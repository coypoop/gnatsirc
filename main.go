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
	PRStartScan = 59240
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
				log.Printf("%v", m)
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
					prState, err := findPRState(prText)
					if err != nil {
						return
					}

					outText = prUrl + " (" + prState + ") " + prSynopsis

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

func prExists(prNum int) bool {
	prUrl := toGnatsUrl(prNum)
	prText, err := getPRText(prUrl)
	if err != nil {
		return false
	}
	_, err = findPRSynopsis(prText)
	if err != nil {
		return false
	}
	return true
}

func findLatestGoodPR() int {
	currentPR := PRStartScan
	latestGoodPR := PRStartScan

	// We allow multiple failed PRs in a row in case people made
	// confidential PRs which look the same as non-existent PRs
	badPRs := 0
	for {
		currentPR++
		if prExists(currentPR) {
			log.Printf("PR number %d exists, resetting bad PR count", currentPR)
			latestGoodPR = currentPR
			badPRs = 0
		} else {
			badPRs++
			log.Printf("PR number %d doesn't exist, bad PR count: %d", currentPR, badPRs)
		}
		if badPRs > 5 {
			return latestGoodPR
		}
	}

}

func observeNewPRs(c *irc.Client, ircChan string) {
	type PrData struct {
		Synopsis string
		Category string
	}
	latestGoodPR := findLatestGoodPR()
	startPR := latestGoodPR + 1
	fmt.Printf("Starting to observe new PRs beginning with %d", startPR)
	for {
		prDatas := make(map[int]PrData)
		for i := 0; i < 20; i++ {
			currentPR := startPR + i
			log.Printf("Checking out %d", currentPR)
			prUrl := toGnatsUrl(currentPR)
			prText, err := getPRText(prUrl)
			if err != nil {
				log.Printf("getPRText returned err %+v for PR URL #%s", err, prUrl)
				continue
			}
			currentSynopsis, err := findPRSynopsis(prText)
			if err != nil {
				log.Printf("findPRSynopsis returned err %v for PR %d (confidential/non-existent bug)", err, currentPR)
				continue
			}
			currentCategory, err := findPRCategory(prText)
			if err != nil {
				log.Printf("findPRCategory returned err %+v for prText %s", err, prText)
				continue
			}
			if !allowedCategory(currentCategory) {
				log.Printf("category %s is not allowed", currentCategory)
				continue
			}
			latestGoodPR = currentPR
			prDatas[currentPR] = PrData{
				Synopsis: currentSynopsis,
				Category: currentCategory,
			}
		}

		startPR = latestGoodPR + 1

		if len(prDatas) > 5 {
			log.Printf("Was going to post >5 new PR messages to chat, skipping")
			log.Printf("Would have printed: %v", prDatas)
			continue
		}

		for prNumber, prData := range prDatas {
			outText := fmt.Sprintf("new %s (%s) %s",
				toGnatsUrl(prNumber), prData.Category, prData.Synopsis)
			c.WriteMessage(&irc.Message{
				Command: "PRIVMSG",
				Params: []string{
					ircChan,
					outText,
				},
			})

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

func findPRState(prText string) (string, error) {
	return findFirstSubmatch(prText, stateRegexp)
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
var stateRegexp *regexp.Regexp
var selfMsgRegexp *regexp.Regexp

func init() {
	selfMsgRegexp = regexp.MustCompile(`https://gnats.netbsd.org`)
	synopsisRegexp = regexp.MustCompile(`.*Synopsis:.... *(.*)`)
	categoryRegexp = regexp.MustCompile(`.*Category:.... *(.*)`)
	stateRegexp = regexp.MustCompile(`.*State:.... *(.*)`)
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
