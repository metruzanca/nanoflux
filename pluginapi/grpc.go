package pluginapi

import (
	"context"
	"errors"
	"net/url"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	pb "github.com/metruzanca/nanoflux/pluginapi/gen"
	"google.golang.org/grpc"
)

// fetcherPlugin adapts a Fetcher to go-plugin's GRPCPlugin interface. On the
// plugin side it serves Impl; on the host side Impl is nil and GRPCClient
// returns the RPC-backed Fetcher.
type fetcherPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	impl Fetcher
}

var _ goplugin.GRPCPlugin = (*fetcherPlugin)(nil)

func (p *fetcherPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterFetcherServer(s, &grpcFetcherServer{impl: p.impl, broker: broker})
	return nil
}

func (p *fetcherPlugin) GRPCClient(ctx context.Context, broker *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return &grpcFetcherClient{client: pb.NewFetcherClient(c), broker: broker}, nil
}

// ---- plugin side: serve the Fetcher, dial the host back for Host ----

type grpcFetcherServer struct {
	pb.UnimplementedFetcherServer
	impl   Fetcher
	broker *goplugin.GRPCBroker
}

func (s *grpcFetcherServer) Meta(context.Context, *pb.MetaRequest) (*pb.MetaResponse, error) {
	m := s.impl.Meta()
	return &pb.MetaResponse{Name: m.Name, ApiVersion: m.APIVersion, RawNetwork: m.RawNetwork}, nil
}

func (s *grpcFetcherServer) Match(_ context.Context, req *pb.MatchRequest) (*pb.MatchResponse, error) {
	u, err := url.Parse(req.Url)
	if err != nil {
		return &pb.MatchResponse{Match: false}, nil
	}
	return &pb.MatchResponse{Match: s.impl.Match(u, Capability(req.Capability))}, nil
}

func (s *grpcFetcherServer) Discover(ctx context.Context, req *pb.DiscoverRequest) (*pb.DiscoverResponse, error) {
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.DiscoverResponse{Error: toPBError(err)}, nil
	}
	cs, err := s.impl.Discover(ctx, req.PageUrl, host)
	if err != nil {
		return &pb.DiscoverResponse{Error: toPBError(err)}, nil
	}
	out := make([]*pb.Candidate, 0, len(cs))
	for _, c := range cs {
		out = append(out, &pb.Candidate{FeedUrl: c.FeedURL, Title: c.Title, IconUrl: c.IconURL, HomeUrl: c.HomeURL})
	}
	return &pb.DiscoverResponse{Candidates: out}, nil
}

func (s *grpcFetcherServer) Fetch(ctx context.Context, req *pb.FetchRequest) (*pb.FetchResponse, error) {
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.FetchResponse{Error: toPBError(err)}, nil
	}
	res, err := s.impl.Fetch(ctx, FetchRequest{
		URL: req.Url, ETag: req.Etag, LastModified: req.LastModified, Config: req.Config,
	}, host)
	if err != nil {
		return &pb.FetchResponse{Error: toPBError(err)}, nil
	}
	return &pb.FetchResponse{
		Feed:         toPBFeed(res.Feed),
		Items:        toPBItems(res.Items),
		Etag:         res.ETag,
		LastModified: res.LastModified,
		NextPageUrl:  res.NextPageURL,
	}, nil
}

// dialHost connects back to the host's Host service over the broker.
func (s *grpcFetcherServer) dialHost(id uint32) (Host, error) {
	conn, err := s.broker.Dial(id)
	if err != nil {
		return nil, err
	}
	return &grpcHostClient{client: pb.NewHostClient(conn)}, nil
}

// ---- host side: the Fetcher the rest of nanoflux sees ----

type grpcFetcherClient struct {
	client pb.FetcherClient
	broker *goplugin.GRPCBroker
}

func (c *grpcFetcherClient) Meta() Meta {
	resp, err := c.client.Meta(context.Background(), &pb.MetaRequest{})
	if err != nil {
		return Meta{}
	}
	return Meta{Name: resp.Name, APIVersion: resp.ApiVersion, RawNetwork: resp.RawNetwork}
}

func (c *grpcFetcherClient) Match(u *url.URL, cap Capability) bool {
	resp, err := c.client.Match(context.Background(), &pb.MatchRequest{Url: u.String(), Capability: int32(cap)})
	if err != nil {
		return false
	}
	return resp.Match
}

func (c *grpcFetcherClient) Discover(ctx context.Context, pageURL string, h Host) ([]Candidate, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.Discover(ctx, &pb.DiscoverRequest{HostServer: id, PageUrl: pageURL})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fromPBError(resp.Error)
	}
	out := make([]Candidate, 0, len(resp.Candidates))
	for _, x := range resp.Candidates {
		out = append(out, Candidate{FeedURL: x.FeedUrl, Title: x.Title, IconURL: x.IconUrl, HomeURL: x.HomeUrl})
	}
	return out, nil
}

func (c *grpcFetcherClient) Fetch(ctx context.Context, req FetchRequest, h Host) (Result, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.Fetch(ctx, &pb.FetchRequest{
		HostServer: id, Url: req.URL, Etag: req.ETag, LastModified: req.LastModified, Config: req.Config,
	})
	if err != nil {
		return Result{}, err
	}
	if resp.Error != nil {
		return Result{}, fromPBError(resp.Error)
	}
	return Result{
		Feed:         fromPBFeed(resp.Feed),
		Items:        fromPBItems(resp.Items),
		ETag:         resp.Etag,
		LastModified: resp.LastModified,
		NextPageURL:  resp.NextPageUrl,
	}, nil
}

// serveHost exposes h to the plugin over the broker for the duration of one
// call, returning the broker id to pass in the request and a stop func.
func (c *grpcFetcherClient) serveHost(h Host) (uint32, func()) {
	id := c.broker.NextId()
	srv := &grpcHostServer{impl: h}
	go c.broker.AcceptAndServe(id, func(opts []grpc.ServerOption) *grpc.Server {
		s := grpc.NewServer(opts...)
		pb.RegisterHostServer(s, srv)
		return s
	})
	return id, func() {}
}

var _ Fetcher = (*grpcFetcherClient)(nil)

// ---- conversions ----

func toPBError(err error) *pb.PluginError {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnsupportedCapability) {
		return &pb.PluginError{Unsupported: true, Message: err.Error()}
	}
	var rl *RateLimit
	if errors.As(err, &rl) {
		return &pb.PluginError{RateLimited: true, StatusCode: int32(rl.Status), RetryAfterMillis: rl.RetryAfter.Milliseconds(), Message: err.Error()}
	}
	var se *StatusError
	if errors.As(err, &se) {
		return &pb.PluginError{StatusCode: int32(se.Code), Message: err.Error()}
	}
	return &pb.PluginError{Message: err.Error()}
}

func fromPBError(e *pb.PluginError) error {
	switch {
	case e.Unsupported:
		return ErrUnsupportedCapability
	case e.RateLimited:
		return &RateLimit{Status: int(e.StatusCode), RetryAfter: time.Duration(e.RetryAfterMillis) * time.Millisecond}
	case e.StatusCode != 0:
		return &StatusError{Code: int(e.StatusCode)}
	default:
		return &plainError{msg: e.Message}
	}
}

type plainError struct{ msg string }

func (e *plainError) Error() string { return e.msg }

func toPBFeed(f Feed) *pb.Feed {
	return &pb.Feed{Title: f.Title, HomeUrl: f.HomeURL, Description: f.Description, ImageUrl: f.ImageURL}
}

func fromPBFeed(f *pb.Feed) Feed {
	if f == nil {
		return Feed{}
	}
	return Feed{Title: f.Title, HomeURL: f.HomeUrl, Description: f.Description, ImageURL: f.ImageUrl}
}

func toPBItems(items []Item) []*pb.Item {
	out := make([]*pb.Item, 0, len(items))
	for _, it := range items {
		encs := make([]*pb.Enclosure, 0, len(it.Enclosures))
		for _, e := range it.Enclosures {
			encs = append(encs, &pb.Enclosure{Url: e.URL, MimeType: e.MIMEType, Length: e.Length})
		}
		out = append(out, &pb.Item{
			Guid: it.GUID, Title: it.Title, Link: it.Link, Summary: it.Summary,
			ImageUrl: it.ImageURL, PublishedAt: it.PublishedAt, Enclosures: encs,
		})
	}
	return out
}

func fromPBItems(items []*pb.Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		encs := make([]Enclosure, 0, len(it.Enclosures))
		for _, e := range it.Enclosures {
			encs = append(encs, Enclosure{URL: e.Url, MIMEType: e.MimeType, Length: e.Length})
		}
		out = append(out, Item{
			GUID: it.Guid, Title: it.Title, Link: it.Link, Summary: it.Summary,
			ImageURL: it.ImageUrl, PublishedAt: it.PublishedAt, Enclosures: encs,
		})
	}
	return out
}
