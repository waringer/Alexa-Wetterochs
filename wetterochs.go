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
	alexa "github.com/mikeflynn/go-alexa/skillserver"
	"github.com/mmcdole/gofeed"
)

const (
	FEEDURL   = "http://www.wettermail.de/wetter/current/wettermail.rss"
	CACHEFILE = "/tmp/.rsscacheWO"
)

var feedCache FeedCache

// Hauptprogramm. Startet den Download des RSS-Feeds und initialisiert
// den HTTP-Handler.
func main() {
	IP := flag.String("ip", "", "ip to bind to")
	Port := flag.Uint("port", 3080, "port to use")
	AppID := flag.String("appid", "", "AppId from Amazon Developer Portal")
	PidFile := flag.String("pid", "/var/run/wetterochs.pid", "pidfile to write")
	flag.Parse()

	if *AppID == "" {
		log.Fatalln("Amazon AppID fehlt!")
	}

	if *PidFile != "" {
		WritePid(*PidFile)
	}

	feedCache.Init()

	log.Println("Erstmaliger Download des Newsfeeds")
	fp := gofeed.NewParser()
	feed, err := fp.ParseURL(FEEDURL)
	if err != nil || len(feed.Items) == 0 {
		log.Fatalln("Konnte Feed nicht abrufen - Abbruch.")
	}
	feedCache.Set(feed)
	ticker := time.NewTicker(15 * time.Minute)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				log.Println("Update des Newsfeeds")
				newfeed, err := fp.ParseURL(FEEDURL)
				if err != nil {
					log.Println("Konnte Newsfeed nicht updaten: ", err.Error())
				} else {
					feedCache.Set(newfeed)
				}
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
	defer close(quit)

	var Applications = map[string]interface{}{
		"/echo/wetterochs": alexa.EchoApplication{
			AppID:    *AppID,
			OnIntent: WetterochsHandler,
			OnLaunch: WetterochsHandler,
		},
	}

	RunAlexa(Applications, *IP, fmt.Sprintf("%d", *Port))
}

func RunAlexa(apps map[string]interface{}, ip, port string) {
	router := mux.NewRouter()
	alexa.Init(apps, router)

	n := negroni.Classic()
	n.UseHandler(router)
	n.Run(ip + ":" + port)
}

// Handler für unseren Newsdienst. Dieser Code wird einmal pro Anfrage
// ausgeführt.
func WetterochsHandler(echoReq *alexa.EchoRequest, echoResp *alexa.EchoResponse) {
	log.Printf("----> Request für Intent %s empfangen, UserID %s\n", echoReq.Request.Intent.Name, echoReq.Session.User.UserID)

	card, speech := feedCache.Get()

	echoResp.OutputSpeechSSML(speech).Card("Wetterochs Wettermail", card)
	log.Printf("<---- Antworte mit %s\n", card)
}

// Da der Newsfeed asynchron im Hintergrund neu geladen wird muss der
// Zugriff synchronisiert werden. FeedCache realisiert das mit einem
// Mutex.
type FeedCache struct {
	lock  sync.RWMutex
	entry CacheEntry
}

type CacheEntry struct {
	Id     string `json:"id"`
	Card   string `json:"card"`
	Speech string `json:"speech"`
}

func (c *FeedCache) Init() {
	c.lock.Lock()
	defer c.lock.Unlock()

	_, err := ioutil.ReadFile(CACHEFILE)
	if err != nil {
		SaveCache(&CacheEntry{})
	}

	err = LoadCache(&c.entry)

	if err != nil {
		log.Fatalf("Unable to load cache : %v", err)
	}
}

func (c *FeedCache) Get() (string, string) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.entry.Card, c.entry.Speech
}

func (c *FeedCache) Set(f *gofeed.Feed) {
	c.lock.Lock()
	defer c.lock.Unlock()

	for _, v := range f.Items {
		if c.entry.Id != v.Published {
			speech := fmt.Sprint(`<speak>`)
			card := fmt.Sprint("Die Wettermail")

			speech = fmt.Sprintf(`%s<break strength="x-strong"/>Wetter Mail vom %s<break strength="x-strong"/>`, speech, v.PublishedParsed.Format(`<say-as interpret-as="date" format="dm">2.1.</say-as> 15:04`))
			card = fmt.Sprintf("%s vom %s ", card, v.PublishedParsed.Format(`02.01. 15:04`))

			desc := regexp.MustCompile(`(?i)\n`).ReplaceAllLiteralString(v.Description, " ")
			desc = regexp.MustCompile(`(?i)\r`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)&nbsp;`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)<p>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)</p>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)<br>`).ReplaceAllLiteralString(desc, " ")
			desc = regexp.MustCompile(`(?i)<br />`).ReplaceAllLiteralString(desc, " ")

			//replace some pharses for better speaking
			desc = regexp.MustCompile(`(?i)d\.h\.`).ReplaceAllLiteralString(desc, "das heisst")
			desc = regexp.MustCompile(`(?i)gfs-modell`).ReplaceAllLiteralString(desc, `<say-as interpret-as="spell-out">GFS</say-as><break time="10ms" />Modell`)
			desc = regexp.MustCompile(`(?i)wetterochs`).ReplaceAllLiteralString(desc, "wetter-ochs")

			for strings.Contains(desc, "  ") {
				desc = strings.Replace(desc, "  ", " ", -1)
			}

			descs := strings.Split(desc, "</a>")
			if len(descs) > 1 {
				speech = fmt.Sprintf(`%s %s<break strength="x-strong"/>%s`, speech, v.Title, descs[1])
				card = fmt.Sprintf("%s\n%s - %s", card, v.Title, descs[1])
			} else {
				speech = fmt.Sprintf(`%s %s<break strength="x-strong"/>%s`, speech, v.Title, desc)
				card = fmt.Sprintf("%s\n%s - %s", card, v.Title, desc)
			}

			speech = fmt.Sprintf("%s</speak>", speech)

			c.entry.Id = v.Published
			c.entry.Card = card
			c.entry.Speech = speech

			SaveCache(&c.entry)
		}

		break
	}
}

func SaveCache(entry *CacheEntry) {
	log.Printf("Saving cache to file: %s\n", CACHEFILE)

	f, err := os.OpenFile(CACHEFILE, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
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

func LoadCache(entry *CacheEntry) error {
	f, err := os.Open(CACHEFILE)
	if err != nil {
		return err
	}

	t := &[]byte{}
	err = json.NewDecoder(f).Decode(t)
	json.Unmarshal(*t, &entry)

	defer f.Close()
	return err
}

func WritePid(PidFile string) {
	pid := os.Getpid()

	f, err := os.OpenFile(PidFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to create pid file : %v", err)
	}
	defer f.Close()

	f.WriteString(fmt.Sprintf("%d", pid))
}
