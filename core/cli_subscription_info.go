//go:build linux && !cgo && cli

package main

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

func validateTUISubscriptionInfo(info *tuiSubscriptionInfo) error {
	if info == nil {
		return nil
	}
	if info.Upload == nil && info.Download == nil && info.Total == nil && info.Expire == nil {
		return errors.New("subscription info has no recognized fields")
	}
	for _, field := range []*int64{info.Upload, info.Download, info.Total, info.Expire} {
		if field != nil && *field < 0 {
			return errors.New("subscription info cannot contain negative values")
		}
	}
	if info.Expire != nil && *info.Expire > 253402300799 {
		return errors.New("subscription expiry is out of range")
	}
	return nil
}

// Subscription-Userinfo is response metadata, not part of the YAML profile.
// Missing or malformed fields stay unknown instead of being reported as zero.
func parseTUISubscriptionInfo(header string) *tuiSubscriptionInfo {
	if len(header) == 0 || len(header) > 4096 {
		return nil
	}
	info := &tuiSubscriptionInfo{}
	for _, field := range strings.Split(header, ";") {
		key, raw, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			info.Upload = &value
		case "download":
			info.Download = &value
		case "total":
			info.Total = &value
		case "expire":
			if value <= 253402300799 {
				info.Expire = &value
			}
		}
	}
	if info.Upload == nil && info.Download == nil && info.Total == nil && info.Expire == nil {
		return nil
	}
	info.FetchedAt = time.Now().UTC()
	return info
}

func tuiSubscriptionUsed(info *tuiSubscriptionInfo) (int64, bool) {
	if info == nil || info.Upload == nil || info.Download == nil {
		return 0, false
	}
	if *info.Upload > math.MaxInt64-*info.Download {
		return math.MaxInt64, true
	}
	return *info.Upload + *info.Download, true
}

func tuiSubscriptionExpiry(info *tuiSubscriptionInfo) string {
	if info == nil || info.Expire == nil {
		return "not provided"
	}
	if *info.Expire == 0 {
		return "no expiry"
	}
	expiry := time.Unix(*info.Expire, 0)
	if time.Now().After(expiry) {
		return "expired " + expiry.Format("2006-01-02")
	}
	return expiry.Format("2006-01-02")
}
