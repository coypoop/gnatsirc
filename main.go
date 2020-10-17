package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/irc.v3"
)

const (
	IRCServer = "chat.freenode.net:6667"
	IRCChan = "#netbsd-code"
	PRStartScan = 55680
)

func main() {
	conn, err := net.Dial("tcp", IRCServer)
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
				c.Write("JOIN " + IRCChan)
				go observeNewPRs(c)
			} else if m.Command == "PRIVMSG" && c.FromChannel(m) {
				if selfMsg(m.Trailing()) {
					return
				}
				prNum, err := findPR(m.Trailing())
				if err == nil {
					var outText string

					prUrl := toGnatsUrl(prNum)
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

	client := irc.NewClient(conn, config)

	err = client.Run()
	go observeNewPRs(client)
	if err != nil {
		log.Fatalln(err)
	}
}

func observeNewPRs(c *irc.Client) {
	latestGoodPR := PRStartScan
	lastPostedPR := PRStartScan
	synopses := make(map[int]string)

	for {
		for currentPR := latestGoodPR; currentPR - latestGoodPR < 20; currentPR++ {
			currentSynopsis, err := findPRSynopsis(fmt.Sprintf("https://gnats.netbsd.org/%d", currentPR))
			if err != nil {
				continue
			}
			latestGoodPR = currentPR
			synopses[currentPR] = currentSynopsis
		}

		// First run, stay silent.
		if (lastPostedPR == PRStartScan) {
			lastPostedPR = latestGoodPR
		}

		// Don't spam too much...
		if (latestGoodPR - lastPostedPR > 5) {
			lastPostedPR = latestGoodPR - 5
		}

		for ; lastPostedPR < latestGoodPR; lastPostedPR++ {
			if synopsis, ok := synopses[lastPostedPR+1]; ok {
				outText := "new " + toGnatsUrl(lastPostedPR+1) + " " + synopsis
				c.WriteMessage(&irc.Message{
					Command: "PRIVMSG",
					Params: []string{
						IRCChan,
						outText,
					},
				})

			}
		}
		time.Sleep(10*time.Minute)
	}
}

func toGnatsUrl(prNum int) string {
	return fmt.Sprintf("https://gnats.netbsd.org/%d", prNum)
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
		prSynopsisHtmlSanitized := rs[1]
		prSynopsis := undoHtmlSanitize(string(prSynopsisHtmlSanitized))
		return prSynopsis, nil
	}
	return "", errors.New("Not found synopsis in body")
}

func findPR(msg string) (int, error) {
	for _, rgx := range prRegexps {
		rs := rgx.FindStringSubmatch(msg)
		if len(rs) > 1 {
			prString := rs[1]
			prNum, err := strconv.Atoi(prString)
			if err != nil {
				return 0, err
			}
			return prNum, nil
		}
	}
	return 0, errors.New("PR number not found")
}

func undoHtmlSanitize(msg string) string {
	msg = strings.ReplaceAll(msg, "&gt;", ">")
	msg = strings.ReplaceAll(msg, "&lt;", "<")
	msg = strings.ReplaceAll(msg, "&amp;", "&")
	msg = strings.ReplaceAll(msg, "&quot;", `"`)

	return msg
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
