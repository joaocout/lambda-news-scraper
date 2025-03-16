package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/gocolly/colly/v2"
	"github.com/joaocout/news-scraper/util"
	"gopkg.in/gomail.v2"
)

type scrapeParams struct {
	Url   string   `json:"url"`
	Xpath string   `json:"xpath"`
	Terms []string `json:"terms"`
}

func readParams(path string) (ret []scrapeParams) {
	file, err := os.Open(path)
	if err != nil {
		log.Fatalf("error opening json file: %v", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("error reading bytes from json file: %v", err)
	}

	err = json.Unmarshal([]byte(b), &ret)
	if err != nil {
		log.Fatalf("error unmarshalling json: %v", err)
	}

	return
}

func scrapeBy(params []scrapeParams) (ret map[string]string) {
	c := colly.NewCollector()
	c.WithTransport(&http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	})

	ret = make(map[string]string)
	for _, s := range params {
		c.OnXML(s.Xpath, func(e *colly.XMLElement) {
			if util.StringContainsAnyOf(e.Text, s.Terms) {
				ret[e.Attr("href")] = e.Text
			}
		})
		c.Visit(s.Url)
	}

	return
}

// receives and deletes single message
// filters out items older than 30 days in message
func getFromQueue(client *sqs.Client, queueUrl string) (ret map[string]string) {
	data, err := client.ReceiveMessage(context.TODO(), &sqs.ReceiveMessageInput{
		QueueUrl:            &queueUrl,
		VisibilityTimeout:   0,
		MaxNumberOfMessages: 1,
	})
	if err != nil {
		log.Fatalf("error receiving message from queue: %v", err)
	}

	if len(data.Messages) > 0 {
		err := json.Unmarshal([]byte(*data.Messages[0].Body), &ret)
		if err != nil {
			log.Fatalf("error unmarshalling json: %v", err)
		}

		_, err = client.DeleteMessage(context.TODO(), &sqs.DeleteMessageInput{
			QueueUrl:      &queueUrl,
			ReceiptHandle: data.Messages[0].ReceiptHandle,
		})
		if err != nil {
			log.Fatalf("error deleting message: %v", err)
		}
	}

	today := time.Now()
	ret = util.MFilter(ret, func(_, v string) bool {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			log.Fatalf("error parsing date: %v", err)
		}
		return today.Sub(d).Hours()/24 <= 30
	})

	return
}

// formats each new: md5(link)_date
// and sends the formatted result
func sendToQueue(sqsClient *sqs.Client, queueUrl string, news map[string]string, prev map[string]string) {
	formattedNews := make(map[string]string)
	date := time.Now().Format(time.DateOnly)
	for k := range news {
		formattedNews[k] = date
	}

	maps.Copy(formattedNews, prev)

	body, err := json.Marshal(formattedNews)
	if err != nil {
		log.Fatalf("error marshalling json: %v", err)
	}

	_, err = sqsClient.SendMessage(context.TODO(), &sqs.SendMessageInput{
		QueueUrl:    &queueUrl,
		MessageBody: aws.String(string(body)),
	})
	if err != nil {
		log.Fatalf("error sending message to queue: %v", err)
	}

}

func sendToEmail(dialer *gomail.Dialer, from string, to string, subject string, news map[string]string) {
	body := ""
	for k, v := range news {
		body += fmt.Sprintf("<h4><a href=\"%s\">%s</a></h4>", k, v)
	}

	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)
	m.SetBody("text/html", body)

	if err := dialer.DialAndSend(m); err != nil {
		log.Fatalf("error sending email: %v", err)
	}
}

func handler(_ events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	SQS_QUEUE_NAME := os.Getenv("SQS_QUEUE_NAME")
	SMTP_SERVER_HOST := os.Getenv("SMTP_SERVER_HOST")
	SMTP_SERVER_PORT, _ := strconv.Atoi(os.Getenv("SMTP_SERVER_PORT"))
	SMTP_SERVER_USER := os.Getenv("SMTP_SERVER_USER")
	SMTP_SERVER_PASS := os.Getenv("SMTP_SERVER_PASS")
	EMAIL_FROM := os.Getenv("EMAIL_FROM")
	EMAIL_TO := os.Getenv("EMAIL_TO")
	EMAIL_SUBJECT := os.Getenv("EMAIL_SUBJECT")

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("error loading AWS config: %v", err)
	}

	sqsClient := sqs.NewFromConfig(cfg)

	queueUrl, err := sqsClient.GetQueueUrl(context.TODO(), &sqs.GetQueueUrlInput{
		QueueName: aws.String(SQS_QUEUE_NAME),
	})
	if err != nil {
		log.Fatalf("error getting queue url: %v", err)
	}

	pwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("error getting pwd: %v", err)
	}

	prevMsg := getFromQueue(sqsClient, *queueUrl.QueueUrl)

	scrapedNews := scrapeBy(readParams(path.Join(pwd, "input.json")))
	log.Printf("scraped: %v\n", scrapedNews)

	filteredNews := scrapedNews
	if len(prevMsg) > 0 {
		filteredNews = util.MFilter(filteredNews,
			func(k string, _ string) bool {
				_, exists := prevMsg[k]
				return !exists
			})
	}
	log.Printf("filtered: %v\n", filteredNews)

	if len(filteredNews) > 0 {
		d := gomail.NewDialer(SMTP_SERVER_HOST, SMTP_SERVER_PORT, SMTP_SERVER_USER, SMTP_SERVER_PASS)
		sendToEmail(d, EMAIL_FROM, EMAIL_TO, EMAIL_SUBJECT, filteredNews)
	}

	sendToQueue(sqsClient, *queueUrl.QueueUrl, filteredNews, prevMsg)

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
	}, nil
}

func main() {
	lambda.Start(handler)
}
