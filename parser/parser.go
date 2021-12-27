package parser

import (
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"github.com/morristai/rarbg-notifier/client"
	c "github.com/morristai/rarbg-notifier/common"
	"io"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func ParseHomePage(preLeaderboard *c.LeaderboardCache, res io.Reader, rarbgCookie, imdbCookie string) (*c.LeaderboardCache, error) {
	var leaderboard c.LeaderboardCache
	leaderboard.VideoList = map[string]*c.VideoInfo{} // avoid panic: assignment to entry in nil map
	leaderboard.Newest9 = map[string]*c.VideoInfo{}
	baseSelectors := "table:nth-child(6) td:nth-child(2)"
	doc, _ := goquery.NewDocumentFromReader(res)
	s := doc.Find(baseSelectors)

	err := parseLeaderBoard(s, &leaderboard, preLeaderboard, imdbCookie)
	if err != nil {
		return nil, err
	}
	err = parseTop9(s, &leaderboard, preLeaderboard, rarbgCookie, imdbCookie)
	if err != nil {
		return nil, err
	}

	if len(leaderboard.VideoList) == 0 {
		log.Fatalln("Parse Leaderboard data failed!")
	} else if len(leaderboard.Newest9) == 0 {
		log.Fatalln("Parse Newest9 data failed!")
	} else {
		log.Println("✅ Parse raw HTML successful")
	}
	leaderboard.Time = time.Now()
	return &leaderboard, nil
}

func parseTop9(s *goquery.Selection, curLeaderboard, preLeaderboard *c.LeaderboardCache, rarbgCookie, imdbCookie string) error {
	var err error
	s.Find("tr:nth-child(2) div:nth-child(1) table tbody tr td a").Each(func(i int, s *goquery.Selection) {
		alreadyExistPre := false
		title, ok := s.Attr("title")
		if ok {
			title = strings.TrimSpace(title) // drop front and end spaces
		} else {
			originalHtml, _ := s.Html() // check error content
			log.Fatalln("Newest top 9 title parse error\n", originalHtml)
		}
		// Exist 1. check already exist in leaderboard
		for k := range curLeaderboard.VideoList {
			if k == title {
				//log.Printf("%s is already in the leader board\n", title)
				curLeaderboard.Newest9[title] = curLeaderboard.VideoList[title]
				alreadyExistPre = true
				break
			}
		}
		if !alreadyExistPre {
			// Exist 2. check already exist in cache
			if m, ok := preLeaderboard.Newest9[title]; ok {
				curLeaderboard.Newest9[title] = m
			} else {
				curLeaderboard.Newest9[title], err = searchTop9FromBoard(title, rarbgCookie, imdbCookie, preLeaderboard)
				log.Println(title, "successful grabbed additional info from search")
			}
		}
	})
	if err != nil {
		return err
	} else {
		return nil
	}
}

func searchTop9FromBoard(title, rarbgCookie, imdbCookie string, preLeaderboard *c.LeaderboardCache) (*c.VideoInfo, error) {
	var tmpLeaderboard c.LeaderboardCache
	tmpLeaderboard.VideoList = map[string]*c.VideoInfo{} // avoid panic: assignment to entry in nil map
	tmpLeaderboard.Newest9 = map[string]*c.VideoInfo{}

	// https://rarbg.to/torrents.php?search=Last.Looks.2021.1080p.WEBRip.DD5.1.x264-NOGRP
	url := fmt.Sprintf("https://rarbg.to/torrents.php?search=%s", title)
	content := client.RequestRarbg(url, rarbgCookie)
	baseSelectors := "table:nth-child(6) td:nth-child(2)"
	doc, _ := goquery.NewDocumentFromReader(content.Body)
	s := doc.Find(baseSelectors)
	err := parseLeaderBoard(s, &tmpLeaderboard, preLeaderboard, imdbCookie)
	if err != nil {
		return nil, err
	}
	return tmpLeaderboard.VideoList[title], nil
}

func parseLeaderBoard(s *goquery.Selection, curLeaderboard, preLeaderboard *c.LeaderboardCache, imdbCookie string) error {
	s.Find("table.lista2t tr.lista2 td:nth-child(2) a:nth-child(1)").Each(func(i int, s *goquery.Selection) {
		var video c.VideoInfo
		title := s.Contents().Text()
		// check already exist in previous leaderboard
		if m, ok := preLeaderboard.VideoList[title]; !ok {
			MatchBasic(title, &video)                                            // title, year, resolution
			MatchGenre(s.Siblings().Text(), &video)                              // genre
			MatchRating(s.Siblings().Text(), &video)                             // rating
			video.Size = s.Parent().SiblingsFiltered("[width=\"100px\"]").Text() // size
			// IMDB URL and Prolific Review
			r, _ := regexp.Compile("^.*imdb=(\\S*)$")
			imdb, ok := s.Siblings().Attr("href")
			if ok {
				imdbCode := r.FindStringSubmatch(imdb)[1] // e.g. tt13207508
				video.ImdbUrl = fmt.Sprintf("https://www.imdb.com/title/%s", imdbCode)

				// TODO: extract imdb cookie, concurrency request
				content := client.RequestImdb(imdbCode, imdbCookie)
				video.ProlificReview.Mean, video.ProlificReview.Std = ParseImdbReview(content.Body)
			}
			// poster URL
			r, _ = regexp.Compile("^.*(https.*jpg).*$")
			posterUrl, ok := s.Attr("onmouseover")
			if ok {
				video.Poster = r.FindStringSubmatch(posterUrl)[1]
			}
			// Rarbg URL
			video.Url, ok = s.Attr("href")
			video.Url = fmt.Sprintf("https://rarbg.to%s", video.Url)
			curLeaderboard.VideoList[title] = &video
		} else {
			log.Println(m.Title, " is already in Previous Leaderboard")
			curLeaderboard.VideoList[title] = m
		}
	})
	return nil
}

func ParseImdbReview(res io.Reader) (float32, float32) {
	var scores []float64
	var sum, mean, sd float64

	doc, err := goquery.NewDocumentFromReader(res)
	if err != nil {
		log.Fatalln(err)
	}

	selectors := "div.lister div.review-container div.ipl-ratings-bar"
	doc.Find(selectors).Each(func(i int, s *goquery.Selection) {
		score, _ := strconv.ParseFloat(s.Find("span").Eq(1).Text(), 64)
		scores = append(scores, score)
	})
	if len(scores) == 0 { // It's possible there's no Review yet
		return 0, 0
	}
	for i := 0; i < len(scores); i++ {
		sum += scores[i]
	}
	scoresLength := float64(len(scores))
	mean = sum / scoresLength
	for j := 0; j < len(scores); j++ {
		sd += math.Pow(scores[j]-mean, 2) // math support float32?
	}
	sd = math.Sqrt(sd / scoresLength)
	sd = math.Round(sd*100) / 100
	mean = math.Round(mean*100) / 100
	return float32(mean), float32(sd)
}
