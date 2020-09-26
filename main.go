package main

import (
	"errors"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"regexp"

	"gopkg.in/irc.v3"
)

func main() {
	conn, err := net.Dial("tcp", "chat.freenode.net:6667")
	if err != nil {
		log.Fatalln(err)
	}

	config := irc.ClientConfig{
		Nick: "gnat",
		Pass: "password",
		User: "username",
		Name: "GNATS urls on demand",
		Handler: irc.HandlerFunc(func(c *irc.Client, m *irc.Message) {
			if m.Command == "001" {
				// 001 is a welcome event, so we join channels there
				c.Write("JOIN #netbsd-code")
			} else if m.Command == "PRIVMSG" && c.FromChannel(m) {
				if selfMsg(m.Trailing()) {
					return
				}
				prNum, prMatched := findPR(m.Trailing())
				if prMatched {
					var outText string

					prUrl := "https://gnats.netbsd.org/" + prNum
					prSynopsis, synopsisErr := findPRSynopsis(prUrl)

					if synopsisErr != nil {
						return
					}

					outText = prUrl + " " + prSynopsis

					c.WriteMessage(&irc.Message{
						Command: "PRIVMSG",
						Params: []string{
							m.Params[0],
							outText,
						},
					})
				}
			}
		}),
	}

	// Create the client
	client := irc.NewClient(conn, config)
	err = client.Run()
	if err != nil {
		log.Fatalln(err)
	}
}

// does it look like a message that we sent?
func selfMsg(msg string) bool {
	rs := selfMsgRegexp.FindStringSubmatch(msg)
	if len(rs) > 1 {
		return true
	}
	return false
}

func findPRSynopsis(prUrl string) (string, error) {
	resp, err := http.Get(prUrl)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	rs := synopsisRegexp.FindSubmatch(body)
	if len(rs) > 1 {
		prSynopsis := rs[1]
		return string(prSynopsis), nil
	}
	return "", errors.New("Not found synopsis in body")
}

func findPR(msg string) (string, bool) {
	for _, rgx := range prRegexps {
		rs := rgx.FindStringSubmatch(msg)
		if len(rs) > 1 {
			return rs[1], true
		}
	}
	return "", false
}

var prRegexps []*regexp.Regexp
var synopsisRegexp *regexp.Regexp
var selfMsgRegexp *regexp.Regexp

func init() {
	selfMsgRegexp = regexp.MustCompile(`https://gnats.netbsd.org`)
	synopsisRegexp = regexp.MustCompile(`.*Synopsis:.... *(.*)`)
	prRegexps = []*regexp.Regexp{
		regexp.MustCompile("PR [a-z]*/([0-9]{4,5})"),
		regexp.MustCompile("PR ([0-9]{4,5})"),
	}
}
