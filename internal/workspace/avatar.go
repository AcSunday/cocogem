package workspace

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// Uploaded avatars are bounded PNG/JPEG data URIs. The MIME in the data URI
// must match the bytes; SVG, remote URLs and executables are rejected.
const (
	MaxAvatarBytes   = 512 << 10
	MaxAvatarSide    = 2048
	MaxAvatarPixels  = 4 << 20
	avatarMIMEPrefix = "data:image/"
)

// IsImageAvatar reports whether value is a fully decoded supported upload.
func IsImageAvatar(value string) bool {
	return validateAvatar(value, true) == nil
}

// ValidateAvatar accepts empty, legacy built-in choices (for old profiles) and
// verified PNG/JPEG uploads. New profiles must additionally use
// ValidateNewAvatar so legacy emoji cannot be introduced again.
func ValidateAvatar(value string) error {
	if value == "" || containsAvatarChoice(value) {
		return nil
	}
	return validateAvatar(value, true)
}

// ValidateNewAvatar rejects legacy emoji choices on create.
func ValidateNewAvatar(value string) error {
	if value == "" {
		return nil
	}
	if containsAvatarChoice(value) {
		return errors.New("avatar must be an uploaded PNG or JPEG image")
	}
	return validateAvatar(value, true)
}

func containsAvatarChoice(value string) bool {
	for _, choice := range AvatarChoices {
		if value == choice {
			return true
		}
	}
	return false
}

type avatarFormat struct {
	mime   string
	width  int
	height int
}

func validateAvatar(value string, mustDecode bool) error {
	if !strings.HasPrefix(value, avatarMIMEPrefix) {
		return errors.New("avatar must be a local uploaded image data URI")
	}
	header, encoded, ok := strings.Cut(strings.TrimPrefix(value, avatarMIMEPrefix), ",")
	if !ok {
		return errors.New("avatar data URI is malformed")
	}
	mime, encoding, ok := strings.Cut(header, ";")
	if !ok || !strings.EqualFold(encoding, "base64") {
		return errors.New("avatar data URI must use base64")
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime != "png" && mime != "jpeg" {
		return errors.New("avatar must be PNG or JPEG")
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return errors.New("avatar base64 is invalid")
	}
	// Reject an oversized payload before allocating the decoded copy.
	if base64.StdEncoding.DecodedLen(len(encoded)) > MaxAvatarBytes+2 {
		return fmt.Errorf("avatar exceeds %d bytes", MaxAvatarBytes)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return errors.New("avatar base64 is invalid")
	}
	if len(data) > MaxAvatarBytes {
		return fmt.Errorf("avatar exceeds %d bytes", MaxAvatarBytes)
	}
	format, err := inspectAvatar(mime, data, mustDecode)
	if err != nil {
		return err
	}
	if format.width < 1 || format.height < 1 || format.width > MaxAvatarSide || format.height > MaxAvatarSide || format.width*format.height > MaxAvatarPixels {
		return fmt.Errorf("avatar dimensions must be within %dx%d", MaxAvatarSide, MaxAvatarSide)
	}
	return nil
}

// inspectAvatar validates dimensions before full decoding, which bounds decoded
// pixel memory while still proving the image has complete data, not just a
// canvas header. Only standard-library PNG/JPEG decoders are trusted.
func inspectAvatar(declared string, data []byte, decode bool) (avatarFormat, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return avatarFormat{}, errors.New("avatar image data is invalid")
	}
	if format != declared {
		return avatarFormat{}, errors.New("avatar data does not match its declared image type")
	}
	if config.Width < 1 || config.Height < 1 || config.Width > MaxAvatarSide || config.Height > MaxAvatarSide || config.Width*config.Height > MaxAvatarPixels {
		return avatarFormat{}, fmt.Errorf("avatar dimensions must be within %dx%d", MaxAvatarSide, MaxAvatarSide)
	}
	if decode {
		decoded, actual, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return avatarFormat{}, errors.New("avatar image is incomplete or corrupt")
		}
		if actual != declared {
			return avatarFormat{}, errors.New("avatar data does not match its declared image type")
		}
		bounds := decoded.Bounds()
		if bounds.Dx() != config.Width || bounds.Dy() != config.Height {
			return avatarFormat{}, errors.New("avatar dimensions changed while decoding")
		}
	}
	return avatarFormat{mime: declared, width: config.Width, height: config.Height}, nil
}
