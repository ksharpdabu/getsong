package getsong

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/xrash/smetrics"

	log "github.com/cihub/seelog"
	"github.com/otium/ytdl"
	"github.com/pkg/errors"
	"gopkg.in/cheggaaa/pb.v1"
)

var ffmpegBinary string
var optionShowProgressBar bool

func init() {
	setLogLevel("info")
	var err error
	ffmpegBinary, err = getFfmpegBinary()
	if err != nil {
		panic(err)
	}
}

// Options allow you to set the artist, title and duration to find the right song.
// You can also set the progress and debugging for the program execution.
type Options struct {
	Title         string
	Artist        string
	Duration      int
	ShowProgress  bool
	Debug         bool
	DoNotDownload bool
}

// GetSong requires passing in the options which requires at least a title.
// If an Artist is provided, it will save it as Artist - Title.mp3
// You can also pass in a duration, and it will try to find a video that
// is within 10 seconds of that duration.
func GetSong(options Options) (savedFilename string, err error) {
	if options.Debug {
		setLogLevel("debug")
	} else {
		setLogLevel("info")
	}
	optionShowProgressBar = options.ShowProgress

	if options.Title == "" {
		err = fmt.Errorf("must enter title")
		return
	}

	searchTerm := options.Title
	if options.Artist != "" {
		searchTerm += " " + options.Artist
		savedFilename = options.Artist
	}
	if savedFilename != "" {
		savedFilename += " - "
	}
	savedFilename += options.Title

	var youtubeID string
	if options.Duration > 0 {
		youtubeID, err = getMusicVideoID(options.Title, searchTerm, 224)
	} else {
		youtubeID, err = getMusicVideoID(options.Title, searchTerm)
	}
	if err != nil {
		err = errors.Wrap(err, "could not get youtube ID")
		return
	}

	if !options.DoNotDownload {
		var fname string
		fname, err = downloadYouTube(youtubeID, savedFilename)
		if err != nil {
			err = errors.Wrap(err, "could not downlaod video")
			return
		}

		err = convertToMp3(fname)
		if err != nil {
			err = errors.Wrap(err, "could not convert video")
			return
		}
	}

	savedFilename += ".mp3"
	return
}

// setLogLevel determines the log level
func setLogLevel(level string) (err error) {
	// https://github.com/cihub/seelog/wiki/Log-levels
	appConfig := `
	<seelog minlevel="` + level + `">
	<outputs formatid="stdout">
	<filter levels="debug,trace">
		<console formatid="debug"/>
	</filter>
	<filter levels="info">
		<console formatid="info"/>
	</filter>
	<filter levels="critical,error">
		<console formatid="error"/>
	</filter>
	<filter levels="warn">
		<console formatid="warn"/>
	</filter>
	</outputs>
	<formats>
		<format id="stdout"   format="%Date %Time [%LEVEL] %File %FuncShort:%Line %Msg %n" />
		<format id="debug"   format="%Date %Time %EscM(37)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
		<format id="info"    format="%Date %Time %EscM(36)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
		<format id="warn"    format="%Date %Time %EscM(33)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
		<format id="error"   format="%Date %Time %EscM(31)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
	</formats>
	</seelog>
	`
	logger, err := log.LoggerFromConfigAsBytes([]byte(appConfig))
	if err != nil {
		return
	}
	log.ReplaceLogger(logger)
	return
}

// convertToMp3 uses ffmpeg to convert to mp3
func convertToMp3(filename string) (err error) {
	filenameWithoutExtension := strings.TrimRight(filename, filepath.Ext(filename))
	// convert to mp3
	cmd := exec.Command(ffmpegBinary, "-i", filename, "-y", filenameWithoutExtension+".mp3")
	_, err = cmd.CombinedOutput()
	if err == nil {
		os.Remove(filename)
	}
	return
}

// downloadYouTube downloads a youtube video and saves using the filename. Returns the filename with the extension.
func downloadYouTube(youtubeID string, filename string) (downloadedFilename string, err error) {
	info, err := ytdl.GetVideoInfo(youtubeID)
	if err != nil {
		err = fmt.Errorf("Unable to fetch video info: %s", err.Error())
		return
	}
	bestQuality := 0
	var format ytdl.Format
	for _, f := range info.Formats {
		if f.VideoEncoding == "" {
			if f.AudioBitrate > bestQuality {
				bestQuality = f.AudioBitrate
				format = f
			}
		}
	}
	if bestQuality == 0 {
		err = fmt.Errorf("No audio available")
		return
	}
	downloadURL, err := info.GetDownloadURL(format)
	log.Debugf("downloading %s", downloadURL)
	if err != nil {
		err = fmt.Errorf("Unable to get download url: %s", err.Error())
		return
	}

	var out io.Writer
	saveFile, err := os.Create(fmt.Sprintf("%s.%s", filename, format.Extension))
	if err != nil {
		return
	}
	downloadedFilename = saveFile.Name()
	out = saveFile
	log.Debugf("downloading %s to %s", info.Title, saveFile.Name())

	var req *http.Request
	req, err = http.NewRequest("GET", downloadURL.String(), nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err == nil {
			err = fmt.Errorf("Received status code %d from download url", resp.StatusCode)
		}
		err = fmt.Errorf("Unable to start download: %s", err.Error())
		return
	}
	defer resp.Body.Close()

	if optionShowProgressBar {
		progressBar := pb.New64(resp.ContentLength)
		progressBar.SetUnits(pb.U_BYTES)
		progressBar.ShowTimeLeft = true
		progressBar.ShowSpeed = true
		progressBar.RefreshRate = 1 * time.Second
		progressBar.Output = os.Stderr
		progressBar.Start()
		defer progressBar.Finish()
		out = io.MultiWriter(out, progressBar)
	}
	_, err = io.Copy(out, resp.Body)
	saveFile.Close()
	if err != nil {
		return
	}

	return
}

// getMusicVideoID returns the ids for a specified title and artist
func getMusicVideoID(title string, titleAndArtist string, expectedDuration ...int) (id string, err error) {
	youtubeSearchURL := fmt.Sprintf(
		`https://www.youtube.com/results?search_query="Provided+to+YouTube"+%s`,
		strings.Join(strings.Fields(titleAndArtist), "+"),
	)
	log.Debugf("searching url: %s", youtubeSearchURL)

	client := &http.Client{}

	req, err := http.NewRequest("GET", youtubeSearchURL, nil)
	if err != nil {
		log.Error(err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Error(err)
		return
	}

	// do this now so it won't be forgotten
	defer resp.Body.Close()
	// reads html as a slice of bytes
	type Track struct {
		Title string
		ID    string
	}
	possibleTracks := []Track{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, "Provided to YouTube") {
			continue
		}
		if !strings.Contains(line, "yt-lockup-title") {
			continue
		}
		durationParts := strings.Split(getStringInBetween(line, "Duration: ", "."), ":")
		if len(durationParts) != 2 {
			continue
		}
		minutes, errExtract := strconv.Atoi(durationParts[0])
		if errExtract != nil {
			log.Error(errExtract)
			continue
		}
		seconds, errExtract := strconv.Atoi(durationParts[1])
		if errExtract != nil {
			log.Error(errExtract)
			continue
		}
		youtubeID := getStringInBetween(line, `/watch?v=`, `"`)
		youtubeTitle := getStringInBetween(line, `title="`, `"`)
		youtubeDuration := minutes*60 + seconds
		if len(expectedDuration) > 0 {
			if math.Abs(float64(expectedDuration[0]-youtubeDuration)) > 20 {
				log.Debugf("'%s' duration (%ds) is different than expected (%ds)", youtubeTitle, youtubeDuration, expectedDuration[0])
				continue
			}
		}
		log.Debugf("Possible track: %s (%s): %ds", youtubeTitle, youtubeID, youtubeDuration)
		possibleTracks = append(possibleTracks, Track{youtubeTitle, youtubeID})
		id = youtubeID
	}
	if len(possibleTracks) == 0 {
		err = fmt.Errorf("could not find any videos that matched")
		return
	}

	bestMetric := 0.0
	bestTrack := 0
	for i := len(possibleTracks) - 1; i >= 0; i-- {
		metric := smetrics.JaroWinkler(title, possibleTracks[i].Title, 0.7, 4)
		metric2 := smetrics.JaroWinkler(titleAndArtist, possibleTracks[i].Title, 0.7, 4)
		if metric2 > metric {
			metric = metric2
		}
		log.Debugf("%s | %s : %2.3f", title, possibleTracks[i].Title, metric)
		if metric > bestMetric {
			bestMetric = metric
			bestTrack = i
		}
	}
	id = possibleTracks[bestTrack].ID
	log.Debugf("Best track for %s: %s (%s)", titleAndArtist, possibleTracks[bestTrack].Title, possibleTracks[bestTrack].ID)
	return
}

// getStringInBetween Returns empty string if no start string found
func getStringInBetween(str string, start string, end string) (result string) {
	s := strings.Index(str, start)
	if s == -1 {
		return
	}
	s += len(start)
	e := strings.Index(str[s:], end)
	return str[s : s+e]
}

var illegalFileNameCharacters = regexp.MustCompile(`[^[a-zA-Z0-9]-_]`)

func sanitizeFileNamePart(part string) string {
	part = strings.Replace(part, "/", "-", -1)
	part = illegalFileNameCharacters.ReplaceAllString(part, "")
	return part
}

func userHomeDir() string {
	if runtime.GOOS == "windows" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		return home
	}
	return os.Getenv("HOME")
}

func getFfmpegBinary() (locationToBinary string, err error) {
	startTime := time.Now()
	defer func() {
		log.Debugf("time taken: %s", time.Since(startTime))
	}()
	cmd := exec.Command("ffmpeg", "-version")
	ffmpegOutput, errffmpeg := cmd.CombinedOutput()
	if errffmpeg == nil && strings.Contains(string(ffmpegOutput), "ffmpeg version") {
		locationToBinary = "ffmpeg"
		return
	}

	// if ffmpeg doesn't exist, then create it
	ffmpegFolder := path.Join(userHomeDir(), ".getsong")
	os.MkdirAll(ffmpegFolder, 0644)

	err = filepath.Walk(ffmpegFolder,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			_, fname := filepath.Split(path)
			fname = strings.TrimRight(fname, filepath.Ext(fname))
			if fname == "ffmpeg" && (filepath.Ext(path) == ".exe" || filepath.Ext(path) == "") {
				locationToBinary = path
			}
			return nil
		})
	if err != nil {
		return
	}
	if locationToBinary != "" {
		return
	}

	urlToDownload := ""
	if runtime.GOOS == "windows" {
		urlToDownload = "https://ffmpeg.zeranoe.com/builds/win64/static/ffmpeg-4.1-win64-static.zip"
	} else {
		panic("os not supported")
	}

	var out io.Writer
	saveFile, err := os.Create(path.Join(ffmpegFolder, "ffmpeg.zip"))
	if err != nil {
		return
	}
	out = saveFile

	var req *http.Request
	req, err = http.NewRequest("GET", urlToDownload, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err == nil {
			err = fmt.Errorf("Received status code %d from download url", resp.StatusCode)
		}
		err = fmt.Errorf("Unable to start download: %s", err.Error())
		return
	}
	defer resp.Body.Close()

	fmt.Println("Downloading ffmpeg...")
	progressBar := pb.New64(resp.ContentLength)
	progressBar.SetUnits(pb.U_BYTES)
	progressBar.ShowTimeLeft = true
	progressBar.ShowSpeed = true
	progressBar.RefreshRate = 1 * time.Second
	progressBar.Output = os.Stderr
	progressBar.Start()
	defer progressBar.Finish()
	out = io.MultiWriter(out, progressBar)
	_, err = io.Copy(out, resp.Body)
	saveFile.Close()
	if err != nil {
		return
	}

	_, err = unzip(path.Join(ffmpegFolder, "ffmpeg.zip"), ffmpegFolder)
	if err == nil {
		os.Remove(path.Join(ffmpegFolder, "ffmpeg.zip"))
	}
	return
}

// unzip will decompress a zip archive, moving all files and folders
// within the zip file (parameter 1) to an output directory (parameter 2).
func unzip(src string, dest string) ([]string, error) {

	var filenames []string

	r, err := zip.OpenReader(src)
	if err != nil {
		return filenames, err
	}
	defer r.Close()

	for _, f := range r.File {

		rc, err := f.Open()
		if err != nil {
			return filenames, err
		}
		defer rc.Close()

		// Store filename/path for returning and using later on
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip. More Info: http://bit.ly/2MsjAWE
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return filenames, fmt.Errorf("%s: illegal file path", fpath)
		}

		filenames = append(filenames, fpath)

		if f.FileInfo().IsDir() {

			// Make Folder
			os.MkdirAll(fpath, os.ModePerm)

		} else {

			// Make File
			if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
				return filenames, err
			}

			outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return filenames, err
			}

			_, err = io.Copy(outFile, rc)

			// Close the file without defer to close before next iteration of loop
			outFile.Close()

			if err != nil {
				return filenames, err
			}

		}
	}
	return filenames, nil
}
