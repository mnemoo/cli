package api

import (
	"context"
	"fmt"
	"sync/atomic"
)

func (c *Client) ListScratch(ctx context.Context, team, game, path string) ([]ScratchObject, error) {
	var objects []ScratchObject
	url := fmt.Sprintf("%s/file/game/%s/%s/scratch/%s", baseURL, team, game, path)
	if err := c.getJSON(ctx, url, &objects); err != nil {
		return nil, fmt.Errorf("list scratch: %w", err)
	}
	return objects, nil
}

func (c *Client) InitUpload(ctx context.Context, body FileUploadBody) (*UploadResponse, error) {
	var resp UploadResponse
	if err := c.postJSON(ctx, baseURL+"/file/upload", body, &resp); err != nil {
		return nil, fmt.Errorf("init upload: %w", err)
	}
	return &resp, nil
}

func (c *Client) InitMultiUpload(ctx context.Context, body FileMultiUploadBody) (*MultiUploadInitResponse, error) {
	var resp MultiUploadInitResponse
	if err := c.postJSON(ctx, baseURL+"/file/multiupload", body, &resp); err != nil {
		return nil, fmt.Errorf("init multiupload: %w", err)
	}
	return &resp, nil
}

func (c *Client) CompleteMultiUpload(ctx context.Context, body FileCompleteBody) (*CompleteResponse, error) {
	var resp CompleteResponse
	if err := c.postJSON(ctx, baseURL+"/file/complete", body, &resp); err != nil {
		return nil, fmt.Errorf("complete multiupload: %w", err)
	}
	return &resp, nil
}

func (c *Client) CopyFile(ctx context.Context, team, game, pathFrom, pathTo string) (*CopyResponse, error) {
	var resp CopyResponse
	body := map[string]string{
		"team":     team,
		"game":     game,
		"pathFrom": pathFrom,
		"pathTo":   pathTo,
	}
	if err := c.postJSON(ctx, baseURL+"/file/copy", body, &resp); err != nil {
		return nil, fmt.Errorf("copy file: %w", err)
	}
	return &resp, nil
}

func (c *Client) MultiCopy(ctx context.Context, body FileMulticopyBody) (*MultiUploadInitResponse, error) {
	var resp MultiUploadInitResponse
	if err := c.postJSON(ctx, baseURL+"/file/multicopy", body, &resp); err != nil {
		return nil, fmt.Errorf("multicopy: %w", err)
	}
	return &resp, nil
}

func (c *Client) DeleteFiles(ctx context.Context, team, game string, paths []string) (*DeleteResponse, error) {
	var resp DeleteResponse
	body := DeleteBody{Team: team, Game: game, Paths: paths}
	if err := c.postJSON(ctx, baseURL+"/file/delete", body, &resp); err != nil {
		return nil, fmt.Errorf("delete files: %w", err)
	}
	return &resp, nil
}

func (c *Client) PublishMath(ctx context.Context, team, game string) (*PublishResponse, error) {
	var resp PublishResponse
	body := PublishBody{Team: team, Game: game}
	if err := c.postJSON(ctx, baseURL+"/file/publish/math", body, &resp); err != nil {
		return nil, fmt.Errorf("publish math: %w", err)
	}
	return &resp, nil
}

func (c *Client) PublishFront(ctx context.Context, team, game string) (*PublishResponse, error) {
	var resp PublishResponse
	body := PublishBody{Team: team, Game: game}
	if err := c.postJSON(ctx, baseURL+"/file/publish/front", body, &resp); err != nil {
		return nil, fmt.Errorf("publish front: %w", err)
	}
	return &resp, nil
}

func (c *Client) Publish(ctx context.Context, team, game, kind string) (*PublishResult, error) {
	endpoint := fmt.Sprintf("%s/file/publish/%s", baseURL, kind)
	body := PublishBody{Team: team, Game: game}
	var result PublishResult
	if err := c.postJSONNoTimeout(ctx, endpoint, body, &result); err != nil {
		return nil, fmt.Errorf("publish %s: %w", kind, err)
	}
	if result.IsError() {
		return &result, nil
	}
	return &result, nil
}

func (c *Client) UploadToS3(ctx context.Context, presignedURL string, data []byte, contentType string) error {
	return c.putBytes(ctx, presignedURL, data, contentType)
}

func (c *Client) UploadToS3WithCounter(ctx context.Context, presignedURL string, data []byte, contentType string, counter *atomic.Int64) error {
	return c.putBytesWithCounter(ctx, presignedURL, data, contentType, counter)
}
