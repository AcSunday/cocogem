package testutil

import (
	"context"
	"errors"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Web supplies captured evidence without making network requests in tests.
type Web struct{}

func (Web) SearchEnabled() bool { return true }

func (Web) Fetch(_ context.Context, url string) (w.Source, error) {
	for _, source := range Sources {
		if url == source {
			return w.Source{Kind: "web", Title: "测试资料", URL: url, Content: "测试样例：稳定身份、有限协作与证据核对。测试内容不能证明真实模型效果。", CapturedAt: w.Now()}, nil
		}
	}
	return w.Source{}, errors.New("unexpected test source")
}

func (web Web) Search(ctx context.Context, _ string) ([]w.Source, error) {
	source, err := web.Fetch(ctx, Sources[0])
	if err != nil {
		return nil, err
	}
	source.Kind = "search_snippet"
	return []w.Source{source}, nil
}
