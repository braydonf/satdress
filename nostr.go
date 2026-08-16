package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip04"
	"fiatjaf.com/nostr/nip19"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/nfnt/resize"
)

const (
	thumbnailWidth  = 160
	thumbnailHeight = 160
)

type Tag []string
type Tags []Tag
type NostrEvent struct {
	ID        string    `json:"id"`
	PubKey    string    `json:"pubkey"`
	CreatedAt time.Time `json:"created_at"`
	Kind      int       `json:"kind"`
	Tags      Tags      `json:"tags"`
	Content   string    `json:"content"`
	Sig       string    `json:"sig"`
}

type ProfileMetadata struct {
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	About       string `json:"about,omitempty"`
	Website     string `json:"website,omitempty"`
	Picture     string `json:"picture,omitempty"`
	Banner      string `json:"banner,omitempty"`
	NIP05       string `json:"nip05,omitempty"`
	LUD16       string `json:"lud16,omitempty"`
}

func ParseMetadata(event nostr.Event) (*ProfileMetadata, error) {
	if event.Kind != 0 {
		return nil, fmt.Errorf("event %s is kind %d, not 0", event.ID, event.Kind)
	}

	var meta ProfileMetadata
	if err := json.Unmarshal([]byte(event.Content), &meta); err != nil {
		cont := event.Content
		if len(cont) > 100 {
			cont = cont[0:99]
		}
		return nil, fmt.Errorf("failed to parse metadata (%s) from event %s: %w", cont, event.ID, err)
	}

	return &meta, nil
}

var nip57Receipt nostr.Event
var zapEventSerializedStr string
var nip57ReceiptRelays []string

func Nip57DescriptionHash(zapEventSerialized string) string {
	hash := sha256.Sum256([]byte(zapEventSerialized))
	hashString := hex.EncodeToString(hash[:])
	return hashString
}

func DecodeBech32(key string) string {
	if _, v, err := nip19.Decode(key); err == nil {
		return v.(string)
	}
	return key
}

func EncodeBech32Note(id string) (string, error) {
	b, err := hex.DecodeString(id)
	if err != nil {
		return "", err
	}

	bits5, err := bech32.ConvertBits(b, 8, 5, true)
	if err != nil {
		return "", err
	}

	return bech32.Encode("note", bits5)
}

func sendMessage(receiverKey string, message string) {

	var relays []string
	var tags nostr.Tags
	reckey := DecodeBech32(receiverKey)

	recpubkey, err := nostr.PubKeyFromHex(reckey)
	if err != nil {
		log.Printf("Error parsing receiverKey: %s. x\n", err.Error())
		return
	}

	tags = append(tags, nostr.Tag{"p", reckey})

	// parse and encrypt content
	privkeyhex := DecodeBech32(s.NostrPrivateKey)
	privkey, err := nostr.SecretKeyFromHex(privkeyhex)
	if err != nil {
		log.Printf("Error parsing privkey: %s. x\n", err.Error())
		return
	}
	pubkey := nostr.GetPublicKey(privkey)

	sharedSecret, err := nip04.ComputeSharedSecret(recpubkey, privkey)
	if err != nil {
		log.Printf("Error computing shared key: %s. x\n", err.Error())
		return
	}

	encryptedMessage, err := nip04.Encrypt(message, sharedSecret)
	if err != nil {
		log.Printf("Error encrypting message: %s. \n", err.Error())
		return
	}

	event := nostr.Event{
		PubKey:    pubkey,
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindEncryptedDirectMessage,
		Tags:      tags,
		Content:   encryptedMessage,
	}
	event.Sign(privkey)
	publishNostrEvent(event, relays)
	log.Printf("%+v\n", event)
}

// Reusable instance of http client
var httpClient = &http.Client{
	Timeout: 5 * time.Second,
}

// addImageToMetaData adds an image to the LNURL metadata
func addImageToProfile(params *UserParams, imageURL string) (err error) {
	// Download and resize profile picture
	picture, contentType, err := DownloadProfilePicture(imageURL)
	if err != nil {
		log.Debug().Str("Downloading profile picture", err.Error()).Msg("Error")
		return err
	}

	// Determine image format
	var ext string
	switch contentType {
	case "image/jpeg":
		ext = "jpeg"
	case "image/png":
		ext = "png"
	case "image/gif":
		ext = "gif"
	default:
		log.Debug().Str("Detecting image format", "unknown format").Msg("Error")
		return fmt.Errorf("Detecting image format: unknown format")
	}

	// Set image metadata in LNURL metadata
	encodedPicture := base64.StdEncoding.EncodeToString(picture)
	params.Image.Ext = ext
	params.Image.DataURI = "data:" + contentType + ";base64," + encodedPicture
	params.Image.Bytes = picture

	return nil
}

func DownloadProfilePicture(url string) ([]byte, string, error) {
	res, err := httpClient.Get(url)
	if err != nil {
		return nil, "", errors.New("failed to download image: " + err.Error())
	}
	defer res.Body.Close()

	contentType := res.Header.Get("Content-Type")
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/gif" {
		return nil, "", errors.New("unsupported image format")
	}

	var img image.Image
	switch contentType {
	case "image/jpeg":
		img, err = jpeg.Decode(res.Body)
	case "image/png":
		img, err = png.Decode(res.Body)
	case "image/gif":
		img, err = gif.Decode(res.Body)
	}
	if err != nil {
		return nil, "", errors.New("failed to decode image: " + err.Error())
	}

	img = resize.Thumbnail(thumbnailWidth, thumbnailHeight, img, resize.Lanczos3)

	buf := new(bytes.Buffer)

	if err := jpeg.Encode(buf, img, nil); err != nil {
		return nil, "", errors.New("failed to encode image: " + err.Error())
	}
	return buf.Bytes(), contentType, nil
}

func publishNostrEvent(ev nostr.Event, relays []string) {
	// Add more relays, remove trailing slashes, and ensure unique relays
	relays = uniqueSlice(cleanUrls(append(relays, Relays...)))

	privkeyhex := DecodeBech32(s.NostrPrivateKey)
	privkey, err := nostr.SecretKeyFromHex(privkeyhex)
	if err != nil {
		log.Printf("Error parsing privkey: %s \n", err.Error())
		return
	}

	ev.Sign(privkey)

	var wg sync.WaitGroup
	wg.Add(len(relays))

	// Create a buffered channel to control the number of active goroutines
	concurrencyLimit := 20
	goroutines := make(chan struct{}, concurrencyLimit)

	// Publish the event to relays
	for _, url := range relays {
		goroutines <- struct{}{}
		go func(url string) {
			defer func() {
				<-goroutines
				wg.Done()
			}()

			var err error
			var conn *nostr.Relay
			maxRetries := 3
			retryDelay := 1 * time.Second

			for i := 0; i < maxRetries; i++ {
				// Set a timeout for connecting to the relay
				connCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				options := nostr.RelayOptions{}
				conn, err = nostr.RelayConnect(connCtx, url, options)
				cancel()

				if err != nil {
					log.Printf("Error connecting to relay %s: %v", url, err)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				}
				defer conn.Close()

				// Set a timeout for publishing to the relay
				pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err = conn.Publish(pubCtx, ev)
				cancel()

				if err != nil {
					log.Printf("Error publishing to relay %s: %v", url, err)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				} else {
					log.Printf("[NOSTR] published to %s: %s", url, "sent")
					break
				}
			}
		}(url)
	}

	wg.Wait()
}

func ExtractNostrRelays(zapEvent nostr.Event) []string {
	relaysTag := zapEvent.Tags.Find("relays")
	log.Printf("Zap relaysTag: %s", relaysTag)

	if relaysTag == nil {
		return []string{}
	}

	// Skip the first element, which is the tag name
	relays := relaysTag[1:]
	log.Printf("Zap relays: %v", relays)

	return relays
}

func CreateNostrReceipt(zapEvent nostr.Event, invoice string) (nostr.Event, error) {
	privkey, err := nostr.SecretKeyFromHex(nostrPrivkeyHex)
	if err != nil {
		log.Error().Err(err).Str("Couldn't parse privkey: ", err.Error())
		return nostr.Event{}, err
	}
	pub := nostr.GetPublicKey(privkey)

	zapEventSerialized, err := json.Marshal(zapEvent)
	if err != nil {
		return nostr.Event{}, err
	}

	ptag := zapEvent.Tags.Find("p")

	nip57Receipt := nostr.Event{
		PubKey:    pub,
		CreatedAt: nostr.Now(),
		Kind:      9735,
		Tags: nostr.Tags{
			ptag,
			[]string{"P", zapEvent.PubKey.Hex()},
			[]string{"bolt11", invoice},
			[]string{"description", string(zapEventSerialized)},
		},
	}

	if eTag := zapEvent.Tags.Find("e"); eTag != nil {
		nip57Receipt.Tags = append(nip57Receipt.Tags, eTag)
	}

	err = nip57Receipt.Sign(privkey)
	if err != nil {
		return nostr.Event{}, err
	}

	return nip57Receipt, nil
}

func uniqueSlice(slice []string) []string {
	keys := make(map[string]bool)
	list := make([]string, 0, len(slice))
	for _, entry := range slice {
		if _, exists := keys[entry]; !exists && entry != "" {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

func cleanUrls(slice []string) []string {
	list := make([]string, 0, len(slice))
	for _, entry := range slice {
		if strings.HasSuffix(entry, "/") {
			entry = entry[:len(entry)-1]
		}
		list = append(list, entry)
	}
	return list
}
