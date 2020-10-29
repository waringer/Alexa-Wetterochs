package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/codegangsta/negroni"
	"github.com/gorilla/mux"
	"github.com/mmcdole/gofeed"
	alexa "gitlab.com/waringer/go-alexa/skillserver"
)

const (
	feedURL   = "http://www.wettermail.de/wetter/current/wettermail.rss"
	cacheFile = "/tmp/.rsscacheWO"
)

// cmdArguments holds the config
type cmdArguments struct {
	appID   *string
	pidFile *string
	ip      *string
	port    *uint
	version *bool
}

var (
	buildstamp string
	githash    string
	feedCache  feedCacheType
)

func main() {
	args := getArguments()

	if *args.version {
		fmt.Println("Build:", buildstamp)
		fmt.Println("Githash:", githash)
		os.Exit(0)
	}

	log.Printf("Alexa-Wetterochs startet %s - %s", buildstamp, githash)

	if *args.appID == "" {
		log.Fatalln("Amazon AppID fehlt!")
	}

	if *args.pidFile != "" {
		writePid(*args.pidFile)
	}

	feedCache.Init()

	log.Println("Erstmaliger Download des Newsfeeds")
	stopTickerSignal := initFeed(gofeed.NewParser())
	defer close(stopTickerSignal)

	var alexaApp = map[string]interface{}{
		"/echo/wetterochs": alexa.EchoApplication{
			AppID:    *args.appID,
			OnIntent: handlerWetterochs,
			OnLaunch: handlerWetterochs,
		},
	}

	runAlexaApp(alexaApp, *args.ip, fmt.Sprintf("%d", *args.port))
}

// initFeed downloads the feed the first time and start a ticker to reread it every 15 minutes
func initFeed(fp *gofeed.Parser) (stopTickerSignal chan struct{}) {
	feed, err := fp.ParseURL(feedURL)
	if err != nil || len(feed.Items) == 0 {
		//log.Fatalln("Konnte Feed nicht abrufen - Abbruch.")
		log.Println("Konnte Feed nicht abrufen - cache only.")
	} else {
	    feedCache.Set(feed)
	}
	
	ticker := time.NewTicker(15 * time.Minute)
	stopTickerSignal = make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				log.Println("Update des Newsfeeds")
				newfeed, err := fp.ParseURL(feedURL)
				if err != nil {
					log.Println("Konnte Newsfeed nicht updaten: ", err.Error())
				} else {
					feedCache.Set(newfeed)
				}
			case <-stopTickerSignal:
				ticker.Stop()
				return
			}
		}
	}()
	return
}

// getArguments reads the command line arguments
func getArguments() (args cmdArguments) {
	args.ip = flag.String("ip", "", "ip to bind to")
	args.port = flag.Uint("port", 3080, "port to use")
	args.appID = flag.String("appid", "", "AppId from Amazon Developer Portal")
	args.pidFile = flag.String("pid", "/var/run/wetterochs.pid", "pidfile to write")
	args.version = flag.Bool("v", false, "prints current version and exit")
	flag.Parse()
	return
}

// runAlexaApp starts the webserver
func runAlexaApp(apps map[string]interface{}, ip, port string) {
	router := mux.NewRouter()
	alexa.Init(apps, router)

	n := negroni.Classic()
	n.UseHandler(router)
	n.Run(ip + ":" + port)
}

// handleWetterochs handles any user requests and generate the response
func handlerWetterochs(echoReq *alexa.EchoRequest, echoResp *alexa.EchoResponse) {
	log.Printf("----> Request für Intent %s empfangen, UserID %s\n", echoReq.Request.Intent.Name, echoReq.Session.User.UserID)

	card, speech := feedCache.Get()

	echoResp.OutputSpeechSSML(speech).Card("Wetterochs Wettermail", card)
	log.Printf("<---- Antworte mit %s\n", card)
}

// Da der Newsfeed asynchron im Hintergrund neu geladen wird muss der Zugriff synchronisiert werden.
// FeedCache realisiert das mit einem Mutex.
type feedCacheType struct {
	lock  sync.RWMutex
	entry cacheEntry
}

type cacheEntry struct {
	ID     string `json:"id"`
	Card   string `json:"card"`
	Speech string `json:"speech"`
}

func (c *feedCacheType) Init() {
	c.lock.Lock()
	defer c.lock.Unlock()

	_, err := ioutil.ReadFile(cacheFile)
	if err != nil {
		saveCache(&cacheEntry{})
	}

	err = loadCache(&c.entry)

	if err != nil {
		log.Fatalf("Unable to load cache : %v", err)
	}
}

func (c *feedCacheType) Get() (string, string) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.entry.Card, c.entry.Speech
}

func (c *feedCacheType) Set(f *gofeed.Feed) {
	c.lock.Lock()
	defer c.lock.Unlock()

	for _, v := range f.Items {
		if c.entry.ID != v.Published {
			speech := fmt.Sprint(`<speak>`)
			card := fmt.Sprint("Die Wettermail")

			speech = fmt.Sprintf(`%s<break strength="x-strong"/>Wetter Mail vom %s<break strength="x-strong"/>`, speech, v.PublishedParsed.Format(`<say-as interpret-as="date" format="dm">2.1.</say-as> 15:04`))
			card = fmt.Sprintf("%s vom %s ", card, v.PublishedParsed.Format(`02.01. 15:04`))

			desc := regexp.MustCompile(`(?i)\n`).ReplaceAllLiteralString(v.Description, " ")
			desc = regexp.MustCompile(`(?i)\r`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)&nbsp;`).ReplaceAllLiteralString(desc, " ")

			//remove quote of old Mail
			descs := strings.Split(desc, "<p> Stefan Ochs Wettermail - ")
			if len(descs) > 1 {
				desc = descs[0]
			}

			desc = regexp.MustCompile(`(?i)<p>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)</p>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)<br>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)<br />`).ReplaceAllLiteralString(desc, " ")

			desc = regexp.MustCompile(`(?i)<a[^<]*</a>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)<`).ReplaceAllLiteralString(desc, " kleiner ")
			desc = regexp.MustCompile(`(?i)>`).ReplaceAllLiteralString(desc, " größer ")

			//replace some pharses for better speaking
			desc = regexp.MustCompile(`(?i)d\.h\.`).ReplaceAllLiteralString(desc, "das heisst")
			desc = regexp.MustCompile(`(?i)gfs-modell`).ReplaceAllLiteralString(desc, `<say-as interpret-as="spell-out">GFS</say-as><break time="10ms" />Modell`)
			desc = regexp.MustCompile(`(?i)wetterochs`).ReplaceAllLiteralString(desc, "wetter-ochs")
			desc = regexp.MustCompile(`(?i)(\d)-(\d)`).ReplaceAllString(desc, "$1 bis $2")

			//remove ad at end of message
			desc = regexp.MustCompile(`(?i)(\* werbung ).*`).ReplaceAllString(desc, " ")

			for strings.Contains(desc, "  ") {
				desc = strings.Replace(desc, "  ", " ", -1)
			}

			speech = fmt.Sprintf(`%s %s<break strength="x-strong"/>%s`, speech, v.Title, desc)
			card = fmt.Sprintf("%s\n%s - %s", card, v.Title, desc)

			speech = fmt.Sprintf("%s</speak>", speech)

			log.Printf("-> speech %s\n", speech)

			c.entry.ID = v.Published
			c.entry.Card = card
			c.entry.Speech = speech

			saveCache(&c.entry)
		}

		break
	}
}

func saveCache(entry *cacheEntry) {
	log.Printf("Saving cache to file: %s\n", cacheFile)

	f, err := os.OpenFile(cacheFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache : %v", err)
	}
	defer f.Close()

	e, err := json.Marshal(entry)
	if err != nil {
		log.Fatalf("Unable to marshal json: %v", err)
	}

	json.NewEncoder(f).Encode(e)
}

func loadCache(entry *cacheEntry) error {
	f, err := os.Open(cacheFile)
	if err != nil {
		return err
	}

	t := &[]byte{}
	err = json.NewDecoder(f).Decode(t)
	json.Unmarshal(*t, &entry)

	defer f.Close()
	return err
}

func writePid(pidFile string) {
	pid := os.Getpid()

	f, err := os.OpenFile(pidFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to create pid file : %v", err)
	}
	defer f.Close()

	f.WriteString(fmt.Sprintf("%d", pid))
}
