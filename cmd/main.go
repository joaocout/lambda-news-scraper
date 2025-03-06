package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/gocolly/colly/v2"
)

type SiteSpec struct {
	url   string
	xpath string
	terms []string
}

func isValid(s string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(strings.ToLower(s), strings.ToLower(t)) {
			return true
		}
	}
	return false
}

func init() {
	_, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("unable to load AWS config: %v", err)
	}
}

func handler(req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var input = make([]SiteSpec, 0)

	i1 := SiteSpec{
		url:   "https://www.saude.df.gov.br/noticias",
		xpath: "//h4/a",
		terms: []string{"carnaval", "secretaria"},
	}
	input = append(input, i1)

	i2 := SiteSpec{
		url:   "https://blog.grancursosonline.com.br/concursos/centro-oeste/distrito-federal/",
		xpath: "//h2/a",
		terms: []string{"vaga"},
	}
	input = append(input, i2)

	i3 := SiteSpec{
		url:   "https://passageirodeprimeira.com/",
		xpath: "//h1[@class='article--title']/a",
		terms: []string{"maceió"},
	}
	input = append(input, i3)

	// i4 := SiteSpec{
	// 	url:   "https://passageirodeprimeira.com/",
	// 	xpath: "//h1[@class='article--title']/a",
	// 	terms: []string{"maceió"},
	// }
	// input = append(input, i4)

	c := colly.NewCollector()
	c.WithTransport(&http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	})

	for _, s := range input {
		c.OnXML(s.xpath, func(e *colly.XMLElement) {
			if isValid(e.Text, s.terms) {
				fmt.Printf("Link: %s\nText: %s\n\n", e.Attr("href"), e.Text)
			}
		})
		c.Visit(s.url)
	}

	c.OnRequest(func(r *colly.Request) {
		fmt.Println("Visiting", r.URL.String())
	})

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
	}, nil
}

func main() {
	lambda.Start(handler)
}
