package pluginapi

import (
	"context"
	"fmt"
	"time"

	pb "github.com/metruzanca/nanoflux/pluginapi/gen"
)

// grpcHostServer serves the Host interface to a plugin over gRPC. The host
// registers one per call, scoped to the calling plugin.
type grpcHostServer struct {
	pb.UnimplementedHostServer
	impl Host
}

func (s *grpcHostServer) Do(ctx context.Context, req *pb.HTTPRequest) (*pb.HTTPResponse, error) {
	resp, err := s.impl.Do(ctx, HTTPRequest{
		Method: req.Method, URL: req.Url, Headers: req.Headers, Body: req.Body,
	})
	if err != nil {
		return nil, err
	}
	return &pb.HTTPResponse{
		Status: int32(resp.Status), Headers: resp.Headers, Body: resp.Body,
		RateLimited: resp.RateLimited, RetryAfterMillis: resp.RetryAfter.Milliseconds(),
	}, nil
}

func (s *grpcHostServer) Now(context.Context, *pb.NowRequest) (*pb.NowResponse, error) {
	return &pb.NowResponse{UnixMillis: s.impl.Now().UnixMilli()}, nil
}

func (s *grpcHostServer) Log(_ context.Context, req *pb.LogRequest) (*pb.LogResponse, error) {
	s.impl.Logf("%s", req.Message)
	return &pb.LogResponse{}, nil
}

// grpcHostClient is the Host a plugin uses to reach nanoflux over gRPC.
type grpcHostClient struct{ client pb.HostClient }

func (c *grpcHostClient) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	resp, err := c.client.Do(ctx, &pb.HTTPRequest{Method: req.Method, Url: req.URL, Headers: req.Headers, Body: req.Body})
	if err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{
		Status: int(resp.Status), Headers: resp.Headers, Body: resp.Body,
		RateLimited: resp.RateLimited, RetryAfter: time.Duration(resp.RetryAfterMillis) * time.Millisecond,
	}, nil
}

func (c *grpcHostClient) Now() time.Time {
	resp, err := c.client.Now(context.Background(), &pb.NowRequest{})
	if err != nil {
		return time.Now().UTC()
	}
	return time.UnixMilli(resp.UnixMillis).UTC()
}

func (c *grpcHostClient) Logf(format string, args ...any) {
	// Keep formatting on the plugin side so the host just logs the string.
	c.client.Log(context.Background(), &pb.LogRequest{Message: fmt.Sprintf(format, args...)})
}

var _ Host = (*grpcHostClient)(nil)
