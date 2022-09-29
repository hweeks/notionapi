package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hweeks/notionapi"
	"github.com/hweeks/notionapi/tohtml"
	"github.com/kjk/fmthtml"
	"github.com/kjk/u"
)

// detect location of https://winmerge.org/
// if present, we can do directory diffs
// only works on windows
func getDiffToolPath() string {
	path, err := exec.LookPath("WinMergeU")
	if err == nil {
		return path
	}
	dir, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(dir, "AppData", "Local", "Programs", "WinMerge", "WinMergeU.exe")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	path, err = exec.LookPath("opendiff")
	if err == nil {
		return path
	}
	return ""
}

func dirDiff(dir1, dir2 string) {
	diffTool := getDiffToolPath()
	// assume opendiff
	cmd := exec.Command(diffTool, dir1, dir2)
	if strings.Contains(diffTool, "WinMergeU") {
		cmd = exec.Command(diffTool, "/r", dir1, dir2)
	}
	err := cmd.Start()
	must(err)
}

func shouldFormat() bool {
	return !flgNoFormat
}

func toHTML2(page *notionapi.Page) (string, []byte) {
	name := tohtml.HTMLFileNameForPage(page)
	c := tohtml.NewConverter(page)
	c.FullHTML = true
	d, _ := c.ToHTML()
	return name, d
}

func toHTML2NotionCompat(page *notionapi.Page) (string, []byte) {
	name := tohtml.HTMLFileNameForPage(page)
	c := tohtml.NewConverter(page)
	c.FullHTML = true
	c.NotionCompat = true
	d, err := c.ToHTML()
	must(err)
	return name, d
}

func idsEqual(id1, id2 string) bool {
	id1 = notionapi.ToDashID(id1)
	id2 = notionapi.ToDashID(id2)
	return id1 == id2
}

func printLastEvent(msg, id string) {
	id = notionapi.ToDashID(id)
	s := eventsPerID[id]
	if s != "" {
		s = ", " + s
	}
	logf("%s%s\n", msg, s)
}

// compare HTML conversion generated by us with the one we get
// from HTML export from Notion
func testToHTML(startPageID string) {
	startPageIDTmp := notionapi.ToNoDashID(startPageID)
	if startPageIDTmp == "" {
		logf("testToHTML: '%s' is not a valid page id\n", startPageID)
		os.Exit(1)
	}

	startPageID = startPageIDTmp
	knownBad := findKnownBadHTML(startPageID)

	referenceFiles := exportPages(startPageID, notionapi.ExportTypeHTML)
	logf("There are %d files in zip file\n", len(referenceFiles))

	client := newClient()

	seenPages := map[string]bool{}
	pages := []*notionapi.NotionID{notionapi.NewNotionID(startPageID)}
	nPage := 0

	hasDirDiff := getDiffToolPath() != ""
	logf("Diff tool: '%s'\n", getDiffToolPath())
	diffDir := filepath.Join(dataDir, "diff")
	expDiffDir := filepath.Join(diffDir, "exp")
	gotDiffDir := filepath.Join(diffDir, "got")
	must(os.MkdirAll(expDiffDir, 0755))
	must(os.MkdirAll(gotDiffDir, 0755))
	u.RemoveFilesInDirMust(expDiffDir)
	u.RemoveFilesInDirMust(gotDiffDir)

	nDifferent := 0

	didPrintRererenceFiles := false
	for len(pages) > 0 {
		pageID := pages[0]
		pages = pages[1:]

		pageIDNormalized := pageID.NoDashID
		if seenPages[pageIDNormalized] {
			continue
		}
		seenPages[pageIDNormalized] = true
		nPage++

		page, err := downloadPage(client, pageID.NoDashID)
		must(err)
		pages = append(pages, page.GetSubPages()...)
		name, pageHTML := toHTML2NotionCompat(page)
		logf("%02d: %s '%s'", nPage, pageID, name)

		var expData []byte
		for refName, d := range referenceFiles {
			if strings.HasSuffix(refName, name) {
				expData = d
				break
			}
		}

		if len(expData) == 0 {
			logf("\n'%s' from '%s' doesn't seem correct as it's not present in referenceFiles\n", name, page.Root().Title)
			logf("Names in referenceFiles:\n")
			if !didPrintRererenceFiles {
				for s := range referenceFiles {
					logf("  %s\n", s)
				}
				didPrintRererenceFiles = true
			}
			continue
		}

		if bytes.Equal(pageHTML, expData) {
			if isPageIDInArray(knownBad, pageID.NoDashID) {
				printLastEvent(" ok (AND ALSO WHITELISTED)", pageID.NoDashID)
				continue
			}
			printLastEvent(" ok", pageID.NoDashID)
			continue
		}

		{
			{
				fileName := fmt.Sprintf("%s.1-from-notion.html", pageID.NoDashID)
				path := filepath.Join(diffDir, fileName)
				writeFileMust(path, expData)
			}
			{
				fileName := fmt.Sprintf("%s.2-mine.html", pageID.NoDashID)
				path := filepath.Join(diffDir, fileName)
				writeFileMust(path, pageHTML)
			}
		}

		expDataFormatted := ppHTML(expData)
		gotDataFormatted := ppHTML(pageHTML)

		if bytes.Equal(expDataFormatted, gotDataFormatted) {
			if isPageIDInArray(knownBad, pageID.NoDashID) {
				logf(" ok after formatting (AND ALSO WHITELISTED)")
				continue
			}
			printLastEvent(", same formatted", pageID.NoDashID)
			continue
		}

		// if we can diff dirs, run through all files and save files that are
		// differetn in in dirs
		fileName := fmt.Sprintf("%s.html", pageID.NoDashID)
		expPath := filepath.Join(expDiffDir, fileName)
		writeFileMust(expPath, expDataFormatted)
		gotPath := filepath.Join(gotDiffDir, fileName)
		writeFileMust(gotPath, gotDataFormatted)
		logf("\nHTML in https://notion.so/%s doesn't match\n", pageID.NoDashID)

		// if has diff tool capable of comparing directories, save files to
		// directory and invoke difftools
		if hasDirDiff {
			nDifferent++
			continue
		}

		if isPageIDInArray(knownBad, pageID.NoDashID) {
			printLastEvent(" doesn't match but whitelisted", pageID.NoDashID)
			continue
		}

		// don't have diff tool capable of diffing directories so
		// display the diff for first failed comparison
		openCodeDiff(expPath, gotPath)
		os.Exit(1)
	}

	if nDifferent > 0 {
		dirDiff(expDiffDir, gotDiffDir)
	}
}

func ppHTML(d []byte) []byte {
	s := fmthtml.Format(d)
	return s
}
