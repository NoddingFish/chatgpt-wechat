package coze

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
)

// UploadFileRequest represents the request for uploading a file to Coze
type UploadFileRequest struct {
	File     io.Reader
	FileName string
}

// UploadFileResponse represents the response from Coze file upload API
type UploadFileResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    struct {
		ID        string `json:"id"`
		Bytes     int64  `json:"bytes"`
		CreatedAt int64  `json:"created_at"`
		FileName  string `json:"file_name"`
	} `json:"data"`
}

// UploadFile uploads a file to Coze and returns the file_id
func (api *API) UploadFile(ctx context.Context, req *UploadFileRequest) (resp *UploadFileResponse, err error) {
	// Create multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", req.FileName)
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}

	_, err = io.Copy(part, req.File)
	if err != nil {
		return nil, fmt.Errorf("failed to copy file: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close writer: %w", err)
	}

	// Create HTTP request
	url := api.c.getHost() + "/v1/files/upload"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Authorization", "Bearer "+api.getSecret())
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	httpClient := &http.Client{}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	resp = &UploadFileResponse{}
	err = json.Unmarshal(respBody, resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.Code != 0 {
		return nil, fmt.Errorf("upload failed with code %d: %s", resp.Code, resp.Message)
	}

	fmt.Printf("[Coze File Upload] Success - File ID: %s, Name: %s, Size: %d bytes\n",
		resp.Data.ID, resp.Data.FileName, resp.Data.Bytes)

	return resp, nil
}

// UploadFileFromURL downloads a file from URL and uploads it to Coze
func (api *API) UploadFileFromURL(ctx context.Context, fileURL string) (fileID string, err error) {
	// Download file from URL
	httpResp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status: %s", httpResp.Status)
	}

	// Extract filename from URL or use default
	fileName := "image.jpg"
	if contentDisposition := httpResp.Header.Get("Content-Disposition"); contentDisposition != "" {
		// Try to extract filename from Content-Disposition header
		// This is a simplified approach
	}

	// Upload to Coze
	uploadReq := &UploadFileRequest{
		File:     httpResp.Body,
		FileName: fileName,
	}

	uploadResp, err := api.UploadFile(ctx, uploadReq)
	if err != nil {
		return "", err
	}

	return uploadResp.Data.ID, nil
}

// UploadFileFromPath uploads a file from local path to Coze
func (api *API) UploadFileFromPath(ctx context.Context, filePath string) (fileID string, err error) {
	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info for name
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}

	// Upload to Coze
	uploadReq := &UploadFileRequest{
		File:     file,
		FileName: fileInfo.Name(),
	}

	uploadResp, err := api.UploadFile(ctx, uploadReq)
	if err != nil {
		return "", err
	}

	return uploadResp.Data.ID, nil
}
