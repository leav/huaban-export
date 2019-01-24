package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/rifflock/lfshook"
	log "github.com/sirupsen/logrus"
)

type Controller struct {
	queue   []func()
	counter int
}

func NewController() *Controller {
	return &Controller{}
}

func (c *Controller) Enqueue(f func()) {
	c.queue = append(c.queue, f)
}

func (c *Controller) Start() {
	for len(c.queue) > 0 {
		c.counter++
		log.Infof("task #%v", c.counter)
		f := c.queue[0]
		c.queue = c.queue[1:]
		f()
	}
	log.Info("Done!")
}

type ResponseJson struct {
	User struct {
		Pins []struct {
			PinId int `json:"pin_id"`
			Board struct {
				Title string `json:"title"`
			} `json:"board"`
			File struct {
				Bucket string `json:"bucket"`
				Key    string `json:"key"`
				Type   string `json:"type"`
			} `json:"file`
			Link string `json:"link"`
		} `json:"pins"`
	} `json:"user"`
}

func main() {

	options := struct {
		url    string
		id     int
		cookie string
	}{}
	flag.StringVar(&options.url, "url", "", "the url you are redirected to after logging in")
	flag.IntVar(&options.id, "id", 0, "the id of your newest pin")
	flag.StringVar(&options.cookie,
		"cookie",
		"",
		"the cookie of your request")
	flag.Parse()

	if len(options.url) == 0 {
		fmt.Print("将登入之后第一个页面的网址，形如 http://login.meiwu.co/abcd123，粘帖在此: ")
		options.url = strings.TrimSpace(readln())
	}
	if len(options.cookie) == 0 {
		fmt.Print("将访问网站的Cookie，形如 referer=http%3A%2F%2Fhuaban.com%2F; sid=abcd123 非常长的一串，粘帖在此: ")
		//fmt.Print("开启浏览器的开发者面板(Developer Console)，进入网络(Network)分项，回到网页中点击“采集”分项，再回到开发者面板网络分项中找到名为“pins/”的网络请求，一般在靠上位置，在这个网络请求的信息中有“Cookie”一项，形如 referer=http%3A%2F%2Fhuaban.com%2F; sid=abcd123 非常长的一串，粘帖在此: ")
		options.cookie = strings.TrimSpace(readln())
	}
	if options.id == 0 {
		fmt.Print("将最新一张采集到的图片ID，或者需要断点续传的图片ID，形如2241192993，粘帖在此: ")
		id, err := strconv.Atoi(strings.TrimSpace(readln()))
		if err != nil {
			log.Errorf("cannot parse input: %v", err)
		}
		options.id = id
	}

	pathMap := lfshook.PathMap{
		log.InfoLevel:  "info.log",
		log.ErrorLevel: "error.log",
	}

	log.AddHook(lfshook.NewHook(
		pathMap,
		&log.JSONFormatter{},
	))

	controller := NewController()

	var fetchPage func(id int) func()
	fetchPage = func(id int) func() {
		return func() {
			lastId := id
			url := fmt.Sprintf("%v/pins/?max=%v&limit=20&wfl=1", options.url, id)
			log := log.WithField("url", url)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				log.Error(err)
				log.Warning("re-enqueuing...")
				controller.Enqueue(fetchPage(id))
				return
			}
			req.Header.Add("Accept", "application/json")
			req.Header.Add("X-Request", "JSON")
			req.Header.Add("X-Requested-With", "XMLHttpRequest")
			req.Header.Add("Cookie", options.cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Errorf(" http.DefaultClient.Do failed: %v", err)
				log.Warning("re-enqueuing...")
				controller.Enqueue(fetchPage(id))
				return
			}
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				log = log.WithField("response status", resp.Status)
				log.Warning("re-enqueuing...")
				controller.Enqueue(fetchPage(id))
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Error("request failed, re-enqueuing...")
				controller.Enqueue(fetchPage(id))
				return
			}
			j, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				log.Errorf("ioutil.ReadAll failed: %v", err)
				return
			}
			var r ResponseJson
			if err := json.Unmarshal(j, &r); err != nil {
				log.Errorf("json.Unmarshal error: %v", err)
				return
			}

			if len(r.User.Pins) == 0 {
				return
			}

			for _, pin := range r.User.Pins {
				fileName := path.Join("exports", pin.Board.Title, fmt.Sprint(pin.PinId))
				switch pin.File.Type {
				case "image/gif":
					fileName += ".gif"
				case "image/jpeg":
					fileName += ".jpg"
				case "image/png":
					fileName += ".png"
				}

				log = log.WithField("fileName", fileName)
				if err := os.MkdirAll(path.Dir(fileName), os.ModePerm); err != nil {
					log.Errorf("unable to create dir %q: %v", path.Dir(fileName), err)
					return
				}
				imageUrl := "http://img.hb.aicdn.com/" + pin.File.Key
				log = log.WithField("imgUrl", imageUrl)

				log.Infof("downloading %v", pin.PinId)

				if err := downloadUrl(imageUrl, fileName); err != nil {
					log.Error(err)
					controller.Enqueue(fetchPage(lastId))
					return
				}

				cmd := exec.Command("./exiftool.exe", "-overwrite_original", "-XPComment="+pin.Link, fileName)
				stdoutStderr, err := cmd.CombinedOutput()
				if err != nil {
					log.Errorf("run command error: %v\n%v", err, string(stdoutStderr))
					if err := logToFile("skipped.log", fmt.Sprint(pin.PinId)); err != nil {
						log.Error(err)
						return
					}
				}

				lastId = pin.PinId
			}

			controller.Enqueue(fetchPage(lastId))
		}

	}

	controller.Enqueue(fetchPage(options.id))
	controller.Start()
}

func readln() string {
	reader := bufio.NewReader(os.Stdin)
	text, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("failed to read stdin: %v", err)
	}
	return text
}

func downloadUrl(url string, fileName string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request failed with status %v", resp.Status)
	}
	defer resp.Body.Close()

	//open a file for writing
	file, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	// Use io.Copy to just dump the response body to the file. This supports huge files
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

func logToFile(fileName string, text string) error {
	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	defer f.Close()

	if _, err = f.WriteString(text + "\n"); err != nil {
		return err
	}

	return nil
}
