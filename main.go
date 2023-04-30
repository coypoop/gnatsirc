package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/irc.v3"
)

const (
	PRStartScan = 55680
)

var (
	allowedCategories categorySlice
	ircChannel        *string
	ircServer         *string
	ircUsername       *string
	ircPassword       string
)

type categorySlice []string

const ctcpVersionReply = "Code available at https://github.com/coypoop/gnatsirc/"

func (i *categorySlice) String() string {
	return "my string representation"
}

func (i *categorySlice) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func main() {
	flag.Var(&allowedCategories, "allow-category", "Only post PRs from these categories.")
	ircServer = flag.String("irc-server", "irc-server", "Which IRC server to connect, for example irc.example.com:6667")
	ircChannel = flag.String("irc-channel", "irc-channel", "Which IRC channel to join, for example #my-channel")
	ircUsername = flag.String("irc-username", "irc-username", "Which username to use on IRC")
	ircPassword = os.Getenv("IRC_PASSWORD")

	flag.Parse()

	if ircServer == nil ||
		ircChannel == nil ||
		ircUsername == nil {
		usage()
	}
	if len(os.Args) < 4 {
		usage()
	}

	config := irc.ClientConfig{
		Nick: *ircUsername,
		Pass: ircPassword,
		User: *ircUsername,
		Name: "GNATS urls on demand",
		Handler: irc.HandlerFunc(func(c *irc.Client, m *irc.Message) {
			if m.Command == "001" {
				log.Printf("Connected to server %s", *ircServer)
				// 001 is a welcome event, so we identify join channels now
				if ircPassword != "" {
					c.WriteMessage(&irc.Message{
						Command: "PRIVMSG",
						Params: []string{
							"NickServ",
							"IDENTIFY " + *ircUsername + " " + ircPassword,
						},
					})
				}
				c.Write("JOIN " + *ircChannel)
				log.Printf("Joined %s", *ircChannel)
				go observeNewPRs(c, *ircChannel)
			} else if m.Command == "PRIVMSG" && c.FromChannel(m) {
				if selfMsg(m.Trailing()) {
					return
				}
				prNum, err := findPR(m.Trailing())
				if err == nil {
					var outText string

					prUrl := toGnatsUrl(prNum)
					prText, err := getPRText(prUrl)
					if err != nil {
						return
					}
					prSynopsis, err := findPRSynopsis(prText)
					if err != nil {
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
			} else if isCTCP(m) {
				requestingUser := m.Prefix.Name
				switch ctcpType(m) {
				case "VERSION":
					c.WriteMessage(ctcpReply(requestingUser, "VERSION", ctcpVersionReply))
					break
				default:
					break
				}
			}
		}),
	}

	for {
		conn, err := net.Dial("tcp", *ircServer)
		if err != nil {
			time.Sleep(1 * time.Minute)
			continue
		}

		client := irc.NewClient(conn, config)
		err = client.Run()
		if err != nil {
			log.Println(err)
		}
	}
}

func observeNewPRs(c *irc.Client, ircChan string) {
	latestGoodPR := PRStartScan
	lastPostedPR := PRStartScan
	synopses := make(map[int]string)

	for {
		for currentPR := latestGoodPR; currentPR-latestGoodPR < 20; currentPR++ {
			prUrl := toGnatsUrl(currentPR)
			prText, err := getPRText(prUrl)
			if err != nil {
				return
			}

			currentSynopsis, err := findPRSynopsis(prText)
			if err != nil {
				continue
			}
			currentCategory, err := findPRCategory(prText)
			if err != nil {
				continue
			}
			if !allowedCategory(currentCategory) {
				continue
			}
			latestGoodPR = currentPR
			synopses[currentPR] = currentSynopsis
		}

		// First run, stay silent.
		if lastPostedPR == PRStartScan {
			lastPostedPR = latestGoodPR
		}

		// Don't spam too much...
		if latestGoodPR-lastPostedPR > 5 {
			lastPostedPR = latestGoodPR - 5
		}

		for ; lastPostedPR < latestGoodPR; lastPostedPR++ {
			if synopsis, ok := synopses[lastPostedPR+1]; ok {
				outText := "new " + toGnatsUrl(lastPostedPR+1) + " " + synopsis
				c.WriteMessage(&irc.Message{
					Command: "PRIVMSG",
					Params: []string{
						ircChan,
						outText,
					},
				})

			}
		}
		time.Sleep(10 * time.Minute)
	}
}

func allowedCategory(testedCategory string) bool {
	if allowedCategories == nil {
		return true
	}

	for _, allowedCategory := range allowedCategories {
		if testedCategory == allowedCategory {
			return true
		}
	}

	return false
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

const ctcpDelimiter = "\001"

func isCTCP(m *irc.Message) bool {
	if m.Command != "PRIVMSG" {
		return false
	}
	message := m.Trailing()
	if !strings.HasPrefix(message, ctcpDelimiter) {
		return false
	}
	if !strings.HasSuffix(message, ctcpDelimiter) {
		return false
	}
	return true
}

func ctcpType(m *irc.Message) string {
	msg := m.Trailing()
	return strings.TrimSuffix(strings.TrimPrefix(msg, ctcpDelimiter), ctcpDelimiter)
}

func ctcpReply(user, ctcpType, reply string) *irc.Message {
	ctcpEscapedReply := ctcpDelimiter + ctcpType + " " + reply + ctcpDelimiter
	return &irc.Message{
		Command: "NOTICE",
		Params: []string{
			user,
			ctcpEscapedReply,
		},
	}
}

func findFirstSubmatch(prText string, rgx *regexp.Regexp) (string, error) {
	rs := rgx.FindStringSubmatch(prText)
	if len(rs) == 0 {
		return "", errors.New("Regex not found in PR body")
	}
	return rs[1], nil
}

func getPRText(prUrl string) (string, error) {
	resp, err := http.Get(prUrl)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return undoHtmlSanitize(string(body)), nil
}

func findPRCategory(prText string) (string, error) {
	return findFirstSubmatch(prText, categoryRegexp)
}

func findPRSynopsis(prText string) (string, error) {
	return findFirstSubmatch(prText, synopsisRegexp)
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
var categoryRegexp *regexp.Regexp
var selfMsgRegexp *regexp.Regexp

func init() {
	selfMsgRegexp = regexp.MustCompile(`https://gnats.netbsd.org`)
	synopsisRegexp = regexp.MustCompile(`.*Synopsis:.... *(.*)`)
	categoryRegexp = regexp.MustCompile(`.*Category:.... *(.*)`)
	prRegexps = []*regexp.Regexp{
		regexp.MustCompile("PR [a-z]*/([0-9]{4,5})"),
		regexp.MustCompile("PR ([0-9]{4,5})"),
		regexp.MustCompile("PR#([0-9]{4,5})"),
		regexp.MustCompile("PR/([0-9]{4,5})"),
		regexp.MustCompile("[^a-z]pr/([0-9]{4,5})"),
		regexp.MustCompile("[^a-z]pr ([0-9]{4,5})"),
		regexp.MustCompile("[^a-z]pr#([0-9]{4,5})"),
		regexp.MustCompile("^pr ([0-9]{4,5})"),
		regexp.MustCompile("^pr#([0-9]{4,5})"),
		regexp.MustCompile("^pr/([0-9]{4,5})"),
	}
}

func usage() {
	fmt.Printf("Usage: [IRC_PASSWORD=password] \t%s -irc-server irc.example.com:6667 -irc-channel -irc-username gnat #netbsd [-allow-category pkg]\n", os.Args[0])
	flag.PrintDefaults()
	os.Exit(1)
}
