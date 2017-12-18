package main

import (
	"database/sql"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/nfnt/resize"

	"github.com/dghubble/go-twitter/twitter"
)

type MiriaClient struct {
	TwitterClient    (*twitter.Client)
	TwitterUserID    string
	SlackClient      (*SlackWebhookClient)
	AWS              (*AWSCredential)
	DB               (*sql.DB)
	ThumbnailDirPath string
}

func NewMiriaClient() *MiriaClient {
	return &MiriaClient{}
}

func (m *MiriaClient) RegisterThumbnailPath(dirPath string) {
	m.ThumbnailDirPath = dirPath
}

func (m *MiriaClient) InitializeTwitterClient(consumerKey, consumerSecret, accessToken, accessTokenSecret string) {
	m.TwitterClient = NewTwitterClient(consumerKey, consumerSecret, accessToken, accessTokenSecret)
	// Get authenticated user's id_str
	user, _, err := m.TwitterClient.Accounts.VerifyCredentials(&twitter.AccountVerifyParams{})
	if err != nil {
		log.Fatal(err)
	}
	m.TwitterUserID = user.IDStr
}

func (m *MiriaClient) InitializeSlackClient(webhookURL string) {
	m.SlackClient = NewSlackWebhookClient(webhookURL)
}

func (m *MiriaClient) InitializeAWSCredential(accessKeyID, secretAccessKey, region, bucketName, basePath string) {
	m.AWS = NewAWSCredential(accessKeyID, secretAccessKey, region, bucketName, basePath)
}

func (m *MiriaClient) InitializeDBConnection(hostname, databaseName, username, password string) {
	db, err := NewMySQLConnection(hostname, databaseName, username, password)
	if err != nil {
		log.Fatal(err)
	}
	m.DB = db
}

func (m *MiriaClient) CollectEvents(processEvent func(*twitter.Event)) {
	demux := twitter.NewSwitchDemux()
	demux.Event = processEvent
	stream, err := m.TwitterClient.Streams.User(&twitter.StreamUserParams{})
	if err != nil {
		log.Fatal(err)
	}
	go demux.HandleChan(stream.Messages)

	// Wait for SIGINT and SIGTERM (HIT CTRL-C)
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	log.Println(<-ch)
	stream.Stop()
}

// JustPostYourFavoritedTweetToSlack just post your favorited tweet's URL to Slack.
func (m *MiriaClient) JustPostYourFavoritedTweetToSlack(event *twitter.Event) {
	eventKind := event.Event
	eventSource := event.Source.IDStr
	// If you favorited a tweet
	if eventKind == "favorite" && eventSource == m.TwitterUserID {
		tweetID := event.TargetObject.IDStr
		tweetUser := event.TargetObject.User.ScreenName
		tweetURL := TweetURL(tweetID, tweetUser)
		log.Printf("You favorited %s.", tweetURL)
		err := m.SlackClient.postMessage(tweetURL)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func (m *MiriaClient) JustPostYourFavoritedTweetWithMediaWhenNotSavedYet(event *twitter.Event) {
	if event.Event != "favorite" {
		return
	}
	// If you favorited a tweet and not saved the images yet
	tweetID := event.TargetObject.IDStr
	tweetUser := event.TargetObject.User.ScreenName
	tweetURL := TweetURL(tweetID, tweetUser)
	log.Printf("You favorited %s.", tweetURL)
	if !m.shouldBeSaved(event) {
		return
	}
	err := m.SlackClient.postMessage(tweetURL)
	if err != nil {
		log.Fatal(err)
	}
}

func (m *MiriaClient) PostYourFavoritedTweetWithMediaAndSaveImages(event *twitter.Event) {
	if event.Event != "favorite" {
		return
	}
	tweetID := event.TargetObject.IDStr
	tweetUser := event.TargetObject.User.ScreenName
	tweetURL := TweetURL(tweetID, tweetUser)
	log.Printf("You favorited %s.", tweetURL)
	if !m.shouldBeSaved(event) {
		return
	}
	err := m.SlackClient.postMessage(tweetURL)
	if err != nil {
		log.Fatal(err)
	}
	tempDir, err := ioutil.TempDir("", "miria")
	if err != nil {
		log.Fatal(err)
	}
	medias := event.TargetObject.ExtendedEntities.Media
	for _, media := range medias {
		// Save image to temporary directory
		downloadURL := media.MediaURLHttps
		filename := path.Base(downloadURL)
		destinationPath := path.Join(tempDir, filename)
		log.Printf("download %s", downloadURL)
		err := download(media.MediaURLHttps, destinationPath)
		if err != nil {
			log.Print(err)
		}

		// Save information to DB
		log.Print("save info")
		m.saveInfoToDB(event.TargetObject, filename)

		// Generate a thumbnail
		log.Print("generate thumbnail")
		err = m.generateThumbnail(destinationPath)
		if err != nil {
			log.Print(err)
		}

		// Save image to S3 bucket
		log.Print("put to S3")
		err = m.AWS.Put(destinationPath)
		if err != nil {
			log.Print(err)
		}

		// Delete temporary image
		log.Print("delete temporary file")
		os.Remove(destinationPath)
	}
	log.Print("congrats! everything was successful!")
}

func (m *MiriaClient) generateThumbnail(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var decodeFunc func(io.Reader) (image.Image, error)
	var encodeFunc func(io.Writer, image.Image) error
	if strings.HasSuffix(filePath, "jpg") {
		decodeFunc = jpeg.Decode
		encodeFunc = func(w io.Writer, img image.Image) error {
			return jpeg.Encode(w, img, nil)
		}
	} else if strings.HasSuffix(filePath, "png") {
		decodeFunc = png.Decode
		encodeFunc = png.Encode
	} else if strings.HasSuffix(filePath, "gif") {
		decodeFunc = gif.Decode
		encodeFunc = func(w io.Writer, img image.Image) error {
			return gif.Encode(w, img, nil)
		}
	}

	// Decode image
	img, err := decodeFunc(file)
	if err != nil {
		return err
	}

	// Generate 128x128 thumbnail
	imgThubnail := resize.Thumbnail(128, 128, img, resize.Lanczos3)

	// Save
	thumbnailPath := path.Join(m.ThumbnailDirPath, path.Base(filePath))
	out, err := os.Create(thumbnailPath)
	if err != nil {
		return err
	}
	defer out.Close()
	err = encodeFunc(out, imgThubnail)
	if err != nil {
		return err
	}
	return nil
}

func (m *MiriaClient) saveInfoToDB(tweet *twitter.Tweet, filename string) {
	res, err := m.DB.Exec("insert into images (filename) values (?)", filename)
	if err != nil {
		log.Fatal(err)
	}
	lastID, err := res.LastInsertId()
	if err != nil {
		log.Fatal(err)
	}
	tweetID := tweet.IDStr
	tweetUser := tweet.User.ScreenName
	tweetURL := TweetURL(tweetID, tweetUser)
	comment := tweet.FullText
	_, err = m.DB.Exec(
		"insert into image_info (image_id, comment, source) values (?, ?, ?)",
		lastID, comment, tweetURL,
	)
	if err != nil {
		log.Fatal(err)
	}
}

func download(url, destination string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer file.Close()
	file.Write(body)
	return nil
}

func (m *MiriaClient) shouldBeSaved(event *twitter.Event) bool {
	tweetID := event.TargetObject.IDStr
	tweetUser := event.TargetObject.User.ScreenName
	tweetURL := TweetURL(tweetID, tweetUser)
	hasMedia := len(event.TargetObject.ExtendedEntities.Media) > 0
	return event.Source.IDStr == m.TwitterUserID && hasMedia && !m.existSource(tweetURL)
}

func (m *MiriaClient) existSource(source string) bool {
	rows, err := m.DB.Query("select count(*) from image_info where source = ?", source)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		if err := rows.Scan(&count); err != nil {
			log.Fatal(err)
		}
	}
	return count > 0
}
