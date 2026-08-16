package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"path/filepath"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/dustin/go-humanize"
	"github.com/fiatjaf/makeinvoice"
	nwc "github.com/braydonf/go-nwc"
	"github.com/gorilla/mux"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/rs/cors"
	"github.com/rs/zerolog"
	flag "github.com/spf13/pflag"
	qrcode "github.com/skip2/go-qrcode"
)

type UserParams struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Kind   string `json:"kind"`

	Host   string `json:"host"`
	Key    string `json:"key"`
	Pak    string `json:"pak"`
	Waki   string `json:"waki"`
	NodeId string `json:"nodeid"`
	Rune   string `json:"rune"`

	MinSendable string `json:"minSendable"`
	MaxSendable string `json:"maxSendable"`

	Npub             string `json:"npub"`
	NotifyZaps       bool   `json:"notifyzaps"`
	NotifyZapComment bool   `json:"notifycomments"`
	NotifyNonZap     bool   `json:"notifynonzaps"`
	Image            struct {
		DataURI string
		Bytes   []byte
		Ext     string
	}
}

type User struct {
	Name string `koanf:"name"`
	Kind string `koanf:"kind"`
	Host string `koanf:"host"`
	Key string `koanf:"key"`
	Pak string `koanf:"pak"`
	Waki string `koanf:"waki"`
	NodeId string `koanf:"nodeid"`
	Rune string `koanf:"rune"`
	NWCSecret string `koanf:"nwcsecret"`
	NWCRelay string `koanf:"nwcrelay"`
}

type Settings struct {
	Host string `koanf:"host"`
	Port string `koanf:"port"`
	Domain string `koanf:"domain"`
	SiteOwnerName string `koanf:"siteownername"`
	SiteOwnerURL string `koanf:"siteownerurl"`
	SiteName string `koanf:"sitename"`
	TorProxyURL string `koanf:"torproxyurl"`
	Users []User `koanf:"users"`
	NostrPrivateKey    string `koanf:"nostrprivatekey"`
	DataDir string `koanf:"datadir"`
	NWC bool `koanf:"nwc"`
	LogLevel string `koanf:"loglevel"`
}

// array of additional relays
var Relays []string

var (
	// Configuration & settings.
	s Settings
	k = koanf.New(".")

	// Username lookup map.
	userMap = make(map[string]User)

	router = mux.NewRouter()
	log    = zerolog.New(os.Stderr).Output(zerolog.ConsoleWriter{Out: os.Stderr})
)

//go:embed templates/user.html
var userHTML string

//go:embed templates/invoice.html
var invoiceHTML string

//go:embed templates/index.html
var indexHTML string

//go:embed static
var static embed.FS

type Response struct {
	Ok      bool        `json:"ok"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func randomString(len int) string {
	bytes := make([]byte, len)

	for i := 0; i < len; i++ {
		bytes[i] = byte(randInt(97, 122))
	}

	return string(bytes)
}

func randInt(min int, max int) int {
	return min + rand.Intn(max-min)
}

func sendError(w http.ResponseWriter, code int, msg string, args ...interface{}) {
	b, _ := json.Marshal(Response{false, fmt.Sprintf(msg, args...), nil})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(b)
}

func getParams(name string) (*UserParams) {
	var params UserParams

	user, ok := userMap[name]

	if ok {
		params.Name = user.Name
		params.Domain = s.Domain
		params.Kind = user.Kind
		params.Host = user.Host
		params.Key = user.Key
		params.Pak = user.Pak
		params.Waki = user.Waki
		params.NodeId = user.NodeId
		params.Rune = user.Rune
	} else {
		return nil
	}

	return &params
}

func init() {
    rand.Seed(time.Now().UnixNano())
}

func main() {
	cache := expirable.NewLRU[string, string](100, nil, time.Millisecond * 1000 * 10)

	f := flag.NewFlagSet("conf", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Println(f.FlagUsages())
		os.Exit(0)
	}

	// Path to one or more config files to load into koanf along with some config params.
	f.StringSlice("conf", []string{"config.yml"}, "path to one or more .yml config files")
	f.String("datadir", "/var/lib/lightning-address", "the path (abs.) to the data directory")
	f.String("host", "0.0.0.0", "the hostname")
	f.String("port", "8080", "the port")
	f.Parse(os.Args[1:])

	// Load the config files provided in the commandline.
	cFiles, _ := f.GetStringSlice("conf")
	for _, c := range cFiles {
		if err := k.Load(file.Provider(c), yaml.Parser()); err != nil {
			log.Fatal().Err(err).Msg("error loading file: %v")
		}
	}

	// Override command over the config file.
	if err := k.Load(posflag.Provider(f, ".", k), nil); err != nil {
		log.Fatal().Err(err).Msg("error loading config: %v")
	}

	// Get the settings.
	k.Unmarshal("", &s)

	// Set the log level.
	if s.LogLevel == "panic" {
		zerolog.SetGlobalLevel(zerolog.PanicLevel)
	} else if (s.LogLevel == "fatal") {
		zerolog.SetGlobalLevel(zerolog.FatalLevel)
	} else if (s.LogLevel == "error") {
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	} else if (s.LogLevel == "warn") {
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	} else if (s.LogLevel == "info") {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	} else if (s.LogLevel == "debug") {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else if (s.LogLevel == "trace") {
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	} else {
		log.Warn().Str("loglevel", s.LogLevel).Msg("unknown log level, using 'info'")
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// Increase default makeinvoice client timeout for Tor.
	makeinvoice.Client = &http.Client{Timeout: 25 * time.Second}

	// Lowercase domain.
	s.Domain = strings.ToLower(s.Domain)

	if s.TorProxyURL != "" {
		makeinvoice.TorProxyURL = s.TorProxyURL
	}

	// Load templates.
	indexTmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		log.Fatal().Err(err).Msg("error loading template")
	}
	userTmpl, err := template.New("user").Parse(userHTML)
	if err != nil {
		log.Fatal().Err(err).Msg("error loading template")
	}
	invoiceTmpl, err := template.New("invoice").Parse(invoiceHTML)
	if err != nil {
		log.Fatal().Err(err).Msg("error loading template")
	}

	// Setup username lookup map.
	for _, user := range s.Users {
		userMap[user.Name] = user
	}

	privkey, err := nostr.SecretKeyFromHex(s.NostrPrivateKey)
	if err != nil {
		log.Fatal().Err(err).Msg("unable to get privkey")
	}

	pubkey := nostr.GetPublicKey(privkey)

	log.Info().Str("pubkey", pubkey.Hex()).Msg("starting nostr with pubkey")

	// Setup NWC daemon.

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, os.Kill)
	defer cancel()

	if s.NWC {
		absdatadir, err := filepath.Abs(s.DataDir)
		if err != nil {
			log.Fatal().Err(err).Msg("absolute path required for datadir")
		}
		dbpath := filepath.Join(absdatadir, "nwc.db")

		nwcParams := nwc.NWCParams {
			PrivateKey: s.NostrPrivateKey,
			PublicKey: pubkey.Hex(),
			Users: make([]nwc.NWCUser, len(s.Users)),
			Logger: &log,
			DBPath: dbpath,
		}

		for i, user := range s.Users {
			privkey, err := nostr.SecretKeyFromHex(user.NWCSecret)
			if err != nil {
				log.Fatal().Err(err).Msg("unable to get user secret")
			}

			pubkey := nostr.GetPublicKey(privkey)

			nwcParams.Users[i].Name = user.Name
			nwcParams.Users[i].NWCSecret = user.NWCSecret
			nwcParams.Users[i].NWCPubKey = pubkey.Hex()
			nwcParams.Users[i].Relay = user.NWCRelay
			nwcParams.Users[i].Kind = user.Kind
			nwcParams.Users[i].Key = user.Key
			nwcParams.Users[i].Host = user.Host
		}

		go nwc.Start(ctx, &nwcParams)
	}

	// TODO Setup API routes for nwc deeplink.

	// Setup API routes.

	router.Path("/.well-known/lnurlp/{user}").Methods("GET").
		HandlerFunc(handleLNURL)

	router.Path("/").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			data := struct {
				SiteName string
				SiteOwnerName string
				SiteOwnerURL string
				Domain string
				Users []string
				MultipleUsers bool
			}{
				SiteName: s.SiteName,
				SiteOwnerName: s.SiteOwnerName,
				SiteOwnerURL: s.SiteOwnerURL,
				Domain: s.Domain,
				Users: make([]string, len(s.Users)),
				MultipleUsers: len(s.Users) > 1,
			}

			for i, user := range s.Users {
				data.Users[i] = user.Name
			}

			err = indexTmpl.Execute(w, data)
			if err != nil {
				log.Fatal().Err(err).Msg("error executing template")
			}
		},
	)

	router.PathPrefix("/static/").Handler(http.FileServer(http.FS(static)))

	router.Path("/u/{name}").Methods("GET").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			name := mux.Vars(r)["name"]

			params := getParams(name)
			if params == nil {
				sendError(w, 404, "user not found")
				return
			}

			data := struct {
				SiteName string
				SiteOwnerName string
				SiteOwnerURL string
				Domain string
				UserName string
			}{
				SiteName: s.SiteName,
				SiteOwnerName: s.SiteOwnerName,
				SiteOwnerURL: s.SiteOwnerURL,
				Domain: s.Domain,
				UserName: name,
			}

			err = userTmpl.Execute(w, data)
			if err != nil {
				sendError(w, 500, "internal error")
				log.Fatal().Err(err).Msg("error executing template")
			}
		},
	)

	router.Path("/u/{name}/qrcode").Methods("GET").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			name := mux.Vars(r)["name"]

			params := getParams(name)
			if params == nil {
				sendError(w, 404, "user not found")
				return
			}

			var png []byte
			png, err := qrcode.Encode("lightning:" + name + "@" + s.Domain,
				qrcode.Medium, 512)

			if err != nil {
				sendError(w, 500, "internal error")
				log.Fatal().Err(err).Msg("error encoding qrcode")
			}

			w.Header().Set("Content-Type", "image/png")
			w.Write(png)
		},
	)

	router.Path("/i/{id}/qrcode").Methods("GET").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			id := mux.Vars(r)["id"]

			bolt11, ok := cache.Get(id)

			if ok {
				var png []byte
				png, err := qrcode.Encode("lightning:" + bolt11,
					qrcode.Medium, 512)

				if err != nil {
					sendError(w, 500, "internal error")
					log.Fatal().Err(err).Msg("error encoding qrcode")
				}

				w.Header().Set("Content-Type", "image/png")
				w.Write(png)
			} else {
				sendError(w, 404, "invoice not found")
			}
		},
	)

	router.Path("/u/{name}/invoice").Methods("GET").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			name := mux.Vars(r)["name"]
			sats, err := strconv.ParseUint(r.URL.Query().Get("sats"), 10, 64)

			if err != nil {
				sats = 1000
			}

			msats := sats * 1000

			comment := r.URL.Query().Get("comment")

			if len(comment) > 639 {
				comment = ""
			}

			params := getParams(name)
			if params == nil {
				sendError(w, 404, "user not found")
				return
			}

			inv, err := makeInvoice(params, msats, "", comment)

			if err != nil {
				sendError(w, 503, "couldn't make an invoice")
				return
			}

			id := randomString(12)

			cache.Add(id, inv)

			data := struct {
				SiteName string
				SiteOwnerName string
				SiteOwnerURL string
				Domain string
				Invoice string
				UserName string
				ID string
				Sats string
				SatsHuman string

			}{
				SiteName: s.SiteName,
				SiteOwnerName: s.SiteOwnerName,
				SiteOwnerURL: s.SiteOwnerURL,
				Domain: s.Domain,
				Invoice: inv,
				UserName: name,
				ID: id,
				Sats: strconv.FormatUint(sats, 10),
				SatsHuman: humanize.Comma(int64(sats)),
			}

			err = invoiceTmpl.Execute(w, data)
			if err != nil {
				sendError(w, 500, "internal error")
				log.Fatal().Err(err).Msg("error executing template")
			}
		},
	)

	go func() {
		srv := &http.Server{
			Handler:      cors.Default().Handler(router),
			Addr:         s.Host + ":" + s.Port,
			WriteTimeout: 15 * time.Second,
			ReadTimeout:  15 * time.Second,
		}

		log.Info().Str("addr", srv.Addr).Msg("listening")

		err = srv.ListenAndServe()

		if err != nil {
			log.Fatal().Err(err).Msg("error starting server")
		}
	}()

	<-ctx.Done()
}
