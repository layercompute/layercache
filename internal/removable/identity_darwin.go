package removable

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os/exec"
	"strings"
	"time"
)

func volumeUUID(ctx context.Context, mount string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/sbin/diskutil", "info", "-plist", mount).Output()
	if err != nil {
		return "", err
	}
	var plist struct {
		Dict struct {
			Entries []struct {
				XMLName xml.Name
				Text    string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.NewDecoder(bytes.NewReader(data)).Decode(&plist); err != nil {
		return "", err
	}
	for i, entry := range plist.Dict.Entries {
		if i+1 == len(plist.Dict.Entries) {
			break
		}
		if entry.XMLName.Local != "key" || entry.Text != "VolumeUUID" {
			continue
		}
		next := plist.Dict.Entries[i+1]
		if next.XMLName.Local == "string" && len(next.Text) == 36 {
			return strings.ToLower(next.Text), nil
		}
	}
	return "", errors.New("macOS did not report a persistent UUID for this volume")
}
