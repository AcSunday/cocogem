package workspace

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func encodeAvatar(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.NRGBA{R: 0x2f, G: 0x81, B: 0x9d, A: 0xff})
	var encoded bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&encoded, img)
	case "jpeg":
		err = jpeg.Encode(&encoded, img, nil)
	default:
		t.Fatalf("unsupported fixture format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func avatarURI(mime string, data []byte) string {
	return "data:image/" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func TestValidateNewAvatarAcceptsPNGAndJPEG(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			uri := avatarURI(format, encodeAvatar(t, format, 2, 2))
			if err := ValidateNewAvatar(uri); err != nil {
				t.Fatalf("valid %s upload rejected: %v", format, err)
			}
			if !IsImageAvatar(uri) {
				t.Fatalf("valid %s upload was not recognized as an image", format)
			}
		})
	}
}

func TestReviewRejectTruncatedAvatar(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	headerOnly := encoded.Bytes()[:33]
	uri := avatarURI("png", headerOnly)
	if _, err := png.Decode(bytes.NewReader(headerOnly)); err == nil {
		t.Fatal("fixture is unexpectedly a complete image")
	}
	if err := ValidateNewAvatar(uri); err == nil {
		t.Fatal("accepted an incomplete PNG containing only its canvas header; uploaded avatar will be broken")
	}
	jpegHeader := encodeAvatar(t, "jpeg", 1, 1)[:64]
	if err := ValidateNewAvatar(avatarURI("jpeg", jpegHeader)); err == nil {
		t.Fatal("accepted a truncated JPEG")
	}
}

func TestValidateAvatarRejectsMIMEAndDataMismatch(t *testing.T) {
	pngData, jpegData := encodeAvatar(t, "png", 2, 2), encodeAvatar(t, "jpeg", 2, 2)
	cases := []struct{ name, value string }{
		{"png declared as jpeg", avatarURI("jpeg", pngData)},
		{"jpeg declared as png", avatarURI("png", jpegData)},
		{"random bytes as png", avatarURI("png", []byte("not an image"))},
		{"missing base64 marker", "data:image/png," + base64.StdEncoding.EncodeToString(pngData)},
		{"whitespace inside base64", "data:image/png;base64," + strings.ReplaceAll(base64.StdEncoding.EncodeToString(pngData), "", " \n")},
		{"no payload", "data:image/png;base64,"},
		{"malformed data uri", "data:image/png;base64"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateNewAvatar(test.value); err == nil {
				t.Fatalf("accepted invalid avatar fixture")
			}
		})
	}
}

func TestValidateAvatarRejectsSVGRemoteAndLegacyChoicesOnCreate(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	for _, value := range []string{
		"https://example.test/avatar.png",
		"file:///tmp/avatar.png",
		"data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(svg),
		"<svg/>",
		"🐱",
		AvatarChoices[0],
	} {
		if err := ValidateNewAvatar(value); err == nil {
			t.Fatalf("new profile accepted non-upload avatar %q", value)
		}
	}
}

func TestLegacyAvatarChoicesRemainReadableOnly(t *testing.T) {
	if err := ValidateAvatar(""); err != nil {
		t.Fatalf("empty avatar must stay valid: %v", err)
	}
	for _, choice := range AvatarChoices {
		if err := ValidateAvatar(choice); err != nil {
			t.Fatalf("legacy avatar %q became unreadable: %v", choice, err)
		}
	}
}

func TestValidateAvatarRejectsOversizeBytesAndDimensions(t *testing.T) {
	valid := encodeAvatar(t, "png", 2, 2)
	padded := append(append([]byte{}, valid...), make([]byte, MaxAvatarBytes+1)...)
	if err := ValidateNewAvatar(avatarURI("png", padded)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized payload error=%v, want an explicit byte limit", err)
	}
	wide := encodeAvatar(t, "png", MaxAvatarSide+1, 1)
	if err := ValidateNewAvatar(avatarURI("png", wide)); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("oversized dimension error=%v, want an explicit side limit", err)
	}
	if err := ValidateNewAvatar(avatarURI("png", encodeAvatar(t, "png", MaxAvatarSide, 1))); err != nil {
		t.Fatalf("maximum accepted side rejected: %v", err)
	}
}
