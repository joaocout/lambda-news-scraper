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
	"sync"
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

func readParams(path string) (ret []scrapeParams, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening json file: %v", err)
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("error reading bytes from json file: %v", err)
	}

	err = json.Unmarshal([]byte(b), &ret)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling json: %v", err)
	}

	return
}

func scrapeBy(paramsList []scrapeParams) (ret map[string]string, err error) {
	c := colly.NewCollector(colly.Async(true))
	c.WithTransport(&http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	})
	err = c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: 8,
	})
	if err != nil {
		return nil, fmt.Errorf("error adding limit to colly collector: %v", err)
	}

	ret = make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range paramsList {
		wg.Add(1)
		params := p

		go func() {
			defer wg.Done()
			c.OnXML(params.Xpath, func(e *colly.XMLElement) {
				if util.StringContainsAnyOf(e.Text, params.Terms) {
					mu.Lock()
					ret[e.Attr("href")] = e.Text
					mu.Unlock()
				}
			})

			err := c.Visit(params.Url)
			if err != nil {
				log.Fatalf("error visiting %s: %v", params.Url, err)
			}
		}()
	}
	c.Wait()
	wg.Wait()
	return
}

// receives and deletes single message
// filters out items older than 30 days before returning
// so that these messages dont get added again to the queue
func getFromQueue(client *sqs.Client, queueUrl string) (ret map[string]string, err error) {
	data, err := client.ReceiveMessage(context.TODO(), &sqs.ReceiveMessageInput{
		QueueUrl:            &queueUrl,
		VisibilityTimeout:   0,
		MaxNumberOfMessages: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("error receiving message from queue: %v", err)
	}

	if len(data.Messages) > 0 {
		err := json.Unmarshal([]byte(*data.Messages[0].Body), &ret)
		if err != nil {
			return nil, fmt.Errorf("error unmarshalling json: %v", err)
		}

		_, err = client.DeleteMessage(context.TODO(), &sqs.DeleteMessageInput{
			QueueUrl:      &queueUrl,
			ReceiptHandle: data.Messages[0].ReceiptHandle,
		})
		if err != nil {
			return nil, fmt.Errorf("error deleting message: %v", err)
		}
	}

	today := time.Now()
	ret, err = util.MFilter(ret, func(_, v string) (bool, error) {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return false, fmt.Errorf("error parsing date: %v", err)
		}
		return today.Sub(d).Hours()/24 <= 30, nil
	})

	return
}

// formatts map of link:date and sends to queue
func sendToQueue(sqsClient *sqs.Client, queueUrl string, news map[string]string, prev map[string]string) error {
	formattedNews := make(map[string]string)
	date := time.Now().Format(time.DateOnly)

	// hashing and trucating to save space in sqs msg
	// small scale so collision shouldnt matter
	for k := range news {
		if h, err := util.HashAndTruncateBy(k, 10); err != nil {
			return fmt.Errorf("error hashing key: %v", err)
		} else {
			formattedNews[h] = date
		}
	}
	maps.Copy(formattedNews, prev)

	body, err := json.Marshal(formattedNews)
	if err != nil {
		return fmt.Errorf("error marshalling json: %v", err)
	}

	_, err = sqsClient.SendMessage(context.TODO(), &sqs.SendMessageInput{
		QueueUrl:    &queueUrl,
		MessageBody: aws.String(string(body)),
	})
	if err != nil {
		return fmt.Errorf("error sending message to queue: %v", err)
	}

	return nil
}

func sendToEmail(dialer *gomail.Dialer, from string, to string, subject string, news map[string]string) error {
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
		return fmt.Errorf("error sending email: %v", err)
	}

	return nil
}

func handler(_ events.APIGatewayProxyRequest) (ret events.APIGatewayProxyResponse, err error) {
	// default return is 500
	ret.StatusCode = http.StatusInternalServerError
	ret.Body = "internal server error"

	SQS_QUEUE_NAME := os.Getenv("SQS_QUEUE_NAME")
	EMAIL_FROM := os.Getenv("EMAIL_FROM")
	EMAIL_TO := os.Getenv("EMAIL_TO")
	EMAIL_SUBJECT := os.Getenv("EMAIL_SUBJECT")
	SMTP_SERVER_USER := os.Getenv("SMTP_SERVER_USER")
	SMTP_SERVER_PASS := os.Getenv("SMTP_SERVER_PASS")
	SMTP_SERVER_HOST := os.Getenv("SMTP_SERVER_HOST")
	SMTP_SERVER_PORT, err := strconv.Atoi(os.Getenv("SMTP_SERVER_PORT"))
	if err != nil {
		return ret, fmt.Errorf("error converting smtp server port to int: %v", err)
	}

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return ret, fmt.Errorf("error loading AWS config: %v", err)
	}
	sqsClient := sqs.NewFromConfig(cfg)

	queueUrl, err := sqsClient.GetQueueUrl(context.TODO(), &sqs.GetQueueUrlInput{
		QueueName: aws.String(SQS_QUEUE_NAME),
	})
	if err != nil {
		return ret, fmt.Errorf("error getting queue url: %v", err)
	}

	pwd, err := os.Getwd()
	if err != nil {
		return ret, fmt.Errorf("error getting pwd: %v", err)
	}

	prevMsg, err := getFromQueue(sqsClient, *queueUrl.QueueUrl)
	if err != nil {
		return ret, fmt.Errorf("error getting from queue: %v", err)
	}

	params, err := readParams(path.Join(pwd, "input.json"))
	if err != nil {
		return ret, fmt.Errorf("error reading params: %v", err)
	}

	scraped, err := scrapeBy(params)
	if err != nil {
		return ret, fmt.Errorf("error scraping: %v", err)
	}
	filtered := scraped
	if len(prevMsg) > 0 {
		filtered, _ = util.MFilter(filtered,
			func(k string, _ string) (bool, error) {
				if h, err := util.HashAndTruncateBy(k, 10); err != nil {
					return false, fmt.Errorf("error hashing key: %v", err)
				} else {
					_, exists := prevMsg[h]
					return !exists, nil
				}
			})
	}
	log.Printf("scraped: %v\n", scraped)
	log.Printf("filtered: %v\n", filtered)

	if len(filtered) > 0 {
		d := gomail.NewDialer(SMTP_SERVER_HOST, SMTP_SERVER_PORT, SMTP_SERVER_USER, SMTP_SERVER_PASS)
		if err = sendToEmail(d, EMAIL_FROM, EMAIL_TO, EMAIL_SUBJECT, filtered); err != nil {
			return ret, fmt.Errorf("error sending msg to email: %v", err)
		}
	}

	if err = sendToQueue(sqsClient, *queueUrl.QueueUrl, filtered, prevMsg); err != nil {
		return ret, fmt.Errorf("error sending msg to queue: %v", err)
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
	}, nil
}

func main() {
	lambda.Start(handler)
}
