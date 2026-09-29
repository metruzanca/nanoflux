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
	_, hasDocs := s.impl.(Docser)
	_, hasFeedAdmin := s.impl.(FeedAdmin)
	_, hasSettings := s.impl.(Configurable)
	return &pb.MetaResponse{
		Name: m.Name, ApiVersion: m.APIVersion, RawNetwork: m.RawNetwork,
		UserAgent: m.UserAgent, Summary: m.Summary, HasDocs: hasDocs,
		ProvisionLabel: m.ProvisionLabel, HasFeedAdmin: hasFeedAdmin,
		HasSettings: hasSettings,
	}, nil
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

func (s *grpcFetcherServer) Render(ctx context.Context, req *pb.RenderRequest) (*pb.RenderResponse, error) {
	// Rendering is optional: a plugin that does not implement Renderer answers
	// ErrUnsupportedCapability, which the host treats as "nothing extra".
	r, ok := s.impl.(Renderer)
	if !ok {
		return &pb.RenderResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.RenderResponse{Error: toPBError(err)}, nil
	}
	m, err := r.Render(ctx, RenderRequest{
		Link: req.Link, Summary: req.Summary, ImageURL: req.ImageUrl, Config: req.Config,
	}, host)
	if err != nil {
		return &pb.RenderResponse{Error: toPBError(err)}, nil
	}
	return &pb.RenderResponse{SourceUrl: m.SourceURL, EmbedSrc: m.EmbedSrc, Gallery: m.Gallery}, nil
}

func (s *grpcFetcherServer) Docs(context.Context, *pb.DocsRequest) (*pb.DocsResponse, error) {
	// Docs is optional: a plugin that does not implement Docser answers
	// ErrUnsupportedCapability, which the host treats as "no documentation".
	d, ok := s.impl.(Docser)
	if !ok {
		return &pb.DocsResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	return &pb.DocsResponse{Docs: d.Docs()}, nil
}

func (s *grpcFetcherServer) SharedKeys(ctx context.Context, req *pb.SharedKeyRequest) (*pb.SharedKeyResponse, error) {
	sk, ok := s.impl.(SharedKeyer)
	if !ok {
		return &pb.SharedKeyResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.SharedKeyResponse{Error: toPBError(err)}, nil
	}
	entries, err := sk.SharedKeys(ctx, SharedKeyRequest{
		FeedURL: req.FeedUrl,
		Feed:    fromPBFeed(req.Feed),
		Items:   fromPBItems(req.Items),
	}, host)
	if err != nil {
		return &pb.SharedKeyResponse{Error: toPBError(err)}, nil
	}
	out := make([]*pb.ItemSharedKey, 0, len(entries))
	for _, e := range entries {
		out = append(out, &pb.ItemSharedKey{Index: int32(e.Index), SharedKey: e.SharedKey})
	}
	return &pb.SharedKeyResponse{Entries: out}, nil
}

func (s *grpcFetcherServer) Enrich(ctx context.Context, req *pb.EnrichRequest) (*pb.EnrichResponse, error) {
	en, ok := s.impl.(Enricher)
	if !ok {
		return &pb.EnrichResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.EnrichResponse{Error: toPBError(err)}, nil
	}
	enriched, err := en.Enrich(ctx, EnrichRequest{Items: fromPBItems(req.Items)}, host)
	if err != nil {
		return &pb.EnrichResponse{Error: toPBError(err)}, nil
	}
	out := make([]*pb.Enriched, 0, len(enriched))
	for _, e := range enriched {
		out = append(out, &pb.Enriched{Index: int32(e.Index), Content: e.Content})
	}
	return &pb.EnrichResponse{Enriched: out}, nil
}

func (s *grpcFetcherServer) Decorate(_ context.Context, req *pb.DecorateRequest) (*pb.DecorateResponse, error) {
	dec, ok := s.impl.(Decoration)
	if !ok {
		return &pb.DecorateResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	// Decorate is view-time and must not do network I/O, so no Host is dialed.
	decs, err := dec.Decorate(context.Background(), DecorateRequest{Items: fromPBItems(req.Items)})
	if err != nil {
		return &pb.DecorateResponse{Error: toPBError(err)}, nil
	}
	out := make([]*pb.Decorated, 0, len(decs))
	for _, d := range decs {
		parts := make([]*pb.SourcePart, 0, len(d.Attribution))
		for _, p := range d.Attribution {
			parts = append(parts, &pb.SourcePart{Text: p.Text, Token: p.Token, Url: p.URL})
		}
		out = append(out, &pb.Decorated{Index: int32(d.Index), Kind: int32(d.Kind), Attribution: parts, ThumbUrl: d.ThumbURL, DedupeKey: d.DedupeKey})
	}
	return &pb.DecorateResponse{Decorations: out}, nil
}

func (s *grpcFetcherServer) CanonicalizeFeedURL(_ context.Context, req *pb.CanonicalizeFeedURLRequest) (*pb.CanonicalizeFeedURLResponse, error) {
	p, ok := s.impl.(URLPolicy)
	if !ok {
		return &pb.CanonicalizeFeedURLResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	return &pb.CanonicalizeFeedURLResponse{Url: p.CanonicalizeFeedURL(req.Url)}, nil
}

func (s *grpcFetcherServer) FeedToken(_ context.Context, req *pb.FeedTokenRequest) (*pb.FeedTokenResponse, error) {
	p, ok := s.impl.(URLPolicy)
	if !ok {
		return &pb.FeedTokenResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	return &pb.FeedTokenResponse{Token: p.FeedToken(req.FeedUrl)}, nil
}

func (s *grpcFetcherServer) Provision(ctx context.Context, req *pb.ProvisionRequest) (*pb.ProvisionResponse, error) {
	pr, ok := s.impl.(Provisioner)
	if !ok {
		return &pb.ProvisionResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.ProvisionResponse{Error: toPBError(err)}, nil
	}
	got, err := pr.Provision(ctx, ProvisionRequest{Title: req.Title}, host)
	if err != nil {
		return &pb.ProvisionResponse{Error: toPBError(err)}, nil
	}
	return &pb.ProvisionResponse{Provisioned: &pb.Provisioned{
		FeedUrl: got.FeedURL, Title: got.Title, HomeUrl: got.HomeURL, Fields: toPBFields(got.Fields),
	}}, nil
}

func (s *grpcFetcherServer) FeedSettings(_ context.Context, req *pb.FeedSettingsRequest) (*pb.FeedSettingsResponse, error) {
	fa, ok := s.impl.(FeedAdmin)
	if !ok {
		return &pb.FeedSettingsResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	return &pb.FeedSettingsResponse{Fields: toPBFields(fa.FeedFields(req.FeedUrl))}, nil
}

func (s *grpcFetcherServer) SettingsSchema(_ context.Context, _ *pb.SettingsSchemaRequest) (*pb.SettingsSchemaResponse, error) {
	c, ok := s.impl.(Configurable)
	if !ok {
		return &pb.SettingsSchemaResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	return &pb.SettingsSchemaResponse{Fields: toPBSettingFields(c.Settings())}, nil
}

func (s *grpcFetcherServer) Configure(_ context.Context, req *pb.ConfigureRequest) (*pb.ConfigureResponse, error) {
	c, ok := s.impl.(Configurable)
	if !ok {
		return &pb.ConfigureResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	c.Configure(req.Values)
	return &pb.ConfigureResponse{}, nil
}

func (s *grpcFetcherServer) FeedAction(ctx context.Context, req *pb.FeedActionRequest) (*pb.FeedActionResponse, error) {
	fa, ok := s.impl.(FeedAdmin)
	if !ok {
		return &pb.FeedActionResponse{Error: toPBError(ErrUnsupportedCapability)}, nil
	}
	host, err := s.dialHost(req.HostServer)
	if err != nil {
		return &pb.FeedActionResponse{Error: toPBError(err)}, nil
	}
	res, err := fa.Action(ctx, FeedActionRequest{
		FeedURL: req.FeedUrl, Action: req.Action, Fields: req.Fields,
	}, host)
	if err != nil {
		return &pb.FeedActionResponse{Error: toPBError(err)}, nil
	}
	return &pb.FeedActionResponse{Result: &pb.FeedActionResult{
		Message: res.Message, Deleted: res.Deleted, Fields: toPBFields(res.Fields),
	}}, nil
}

// toPBFields converts display fields to the wire form.
func toPBFields(fields []Field) []*pb.Field {
	out := make([]*pb.Field, 0, len(fields))
	for _, f := range fields {
		out = append(out, &pb.Field{Name: f.Name, Label: f.Label, Value: f.Value, Kind: f.Kind})
	}
	return out
}

func fromPBFields(fields []*pb.Field) []Field {
	out := make([]Field, 0, len(fields))
	for _, f := range fields {
		out = append(out, Field{Name: f.Name, Label: f.Label, Value: f.Value, Kind: f.Kind})
	}
	return out
}

// toPBSettingFields / fromPBSettingFields convert a plugin's settings schema
// across the wire.
func toPBSettingFields(fields []SettingField) []*pb.SettingField {
	out := make([]*pb.SettingField, 0, len(fields))
	for _, f := range fields {
		out = append(out, &pb.SettingField{
			Name: f.Name, Label: f.Label, Kind: f.Kind,
			Placeholder: f.Placeholder, Help: f.Help, Required: f.Required,
		})
	}
	return out
}

func fromPBSettingFields(fields []*pb.SettingField) []SettingField {
	out := make([]SettingField, 0, len(fields))
	for _, f := range fields {
		out = append(out, SettingField{
			Name: f.Name, Label: f.Label, Kind: f.Kind,
			Placeholder: f.Placeholder, Help: f.Help, Required: f.Required,
		})
	}
	return out
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
	return Meta{
		Name: resp.Name, APIVersion: resp.ApiVersion, RawNetwork: resp.RawNetwork,
		UserAgent: resp.UserAgent, Summary: resp.Summary, HasDocs: resp.HasDocs,
		ProvisionLabel: resp.ProvisionLabel, HasFeedAdmin: resp.HasFeedAdmin,
		HasSettings: resp.HasSettings,
	}
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

func (c *grpcFetcherClient) Render(ctx context.Context, req RenderRequest, h Host) (Media, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.Render(ctx, &pb.RenderRequest{
		HostServer: id, Link: req.Link, Summary: req.Summary, ImageUrl: req.ImageURL, Config: req.Config,
	})
	if err != nil {
		return Media{}, err
	}
	if resp.Error != nil {
		return Media{}, fromPBError(resp.Error)
	}
	return Media{SourceURL: resp.SourceUrl, EmbedSrc: resp.EmbedSrc, Gallery: resp.Gallery}, nil
}

// Docs fetches the plugin's Markdown documentation over the wire. An
// unsupported answer maps to ErrUnsupportedCapability.
func (c *grpcFetcherClient) Docs() string {
	resp, err := c.client.Docs(context.Background(), &pb.DocsRequest{})
	if err != nil {
		return ""
	}
	if resp.Error != nil {
		return ""
	}
	return resp.Docs
}

func (c *grpcFetcherClient) SharedKeys(ctx context.Context, req SharedKeyRequest, h Host) ([]ItemSharedKey, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.SharedKeys(ctx, &pb.SharedKeyRequest{
		HostServer: id, FeedUrl: req.FeedURL, Feed: toPBFeed(req.Feed), Items: toPBItems(req.Items),
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fromPBError(resp.Error)
	}
	out := make([]ItemSharedKey, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, ItemSharedKey{Index: int(e.Index), SharedKey: e.SharedKey})
	}
	return out, nil
}

func (c *grpcFetcherClient) Enrich(ctx context.Context, req EnrichRequest, h Host) ([]Enriched, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.Enrich(ctx, &pb.EnrichRequest{HostServer: id, Items: toPBItems(req.Items)})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fromPBError(resp.Error)
	}
	out := make([]Enriched, 0, len(resp.Enriched))
	for _, e := range resp.Enriched {
		out = append(out, Enriched{Index: int(e.Index), Content: e.Content})
	}
	return out, nil
}

func (c *grpcFetcherClient) Decorate(ctx context.Context, req DecorateRequest) ([]Decorated, error) {
	resp, err := c.client.Decorate(ctx, &pb.DecorateRequest{Items: toPBItems(req.Items)})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fromPBError(resp.Error)
	}
	out := make([]Decorated, 0, len(resp.Decorations))
	for _, d := range resp.Decorations {
		parts := make([]SourcePart, 0, len(d.Attribution))
		for _, p := range d.Attribution {
			parts = append(parts, SourcePart{Text: p.Text, Token: p.Token, URL: p.Url})
		}
		out = append(out, Decorated{Index: int(d.Index), Kind: ItemKind(d.Kind), Attribution: parts, ThumbURL: d.ThumbUrl, DedupeKey: d.DedupeKey})
	}
	return out, nil
}

func (c *grpcFetcherClient) CanonicalizeFeedURL(raw string) string {
	resp, err := c.client.CanonicalizeFeedURL(context.Background(), &pb.CanonicalizeFeedURLRequest{Url: raw})
	if err != nil || resp.Error != nil {
		return raw
	}
	return resp.Url
}

func (c *grpcFetcherClient) FeedToken(feedURL string) string {
	resp, err := c.client.FeedToken(context.Background(), &pb.FeedTokenRequest{FeedUrl: feedURL})
	if err != nil || resp.Error != nil {
		return ""
	}
	return resp.Token
}

func (c *grpcFetcherClient) Provision(ctx context.Context, req ProvisionRequest, h Host) (Provisioned, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.Provision(ctx, &pb.ProvisionRequest{HostServer: id, Title: req.Title})
	if err != nil {
		return Provisioned{}, err
	}
	if resp.Error != nil {
		return Provisioned{}, fromPBError(resp.Error)
	}
	p := resp.Provisioned
	if p == nil {
		return Provisioned{}, nil
	}
	return Provisioned{
		FeedURL: p.FeedUrl, Title: p.Title, HomeURL: p.HomeUrl, Fields: fromPBFields(p.Fields),
	}, nil
}

func (c *grpcFetcherClient) Settings() []SettingField {
	resp, err := c.client.SettingsSchema(context.Background(), &pb.SettingsSchemaRequest{})
	if err != nil || resp.Error != nil {
		return nil
	}
	return fromPBSettingFields(resp.Fields)
}

func (c *grpcFetcherClient) Configure(values map[string]string) {
	_, _ = c.client.Configure(context.Background(), &pb.ConfigureRequest{Values: values})
}

func (c *grpcFetcherClient) FeedFields(feedURL string) []Field {
	resp, err := c.client.FeedSettings(context.Background(), &pb.FeedSettingsRequest{FeedUrl: feedURL})
	if err != nil || resp.Error != nil {
		return nil
	}
	return fromPBFields(resp.Fields)
}

func (c *grpcFetcherClient) Action(ctx context.Context, req FeedActionRequest, h Host) (FeedActionResult, error) {
	id, stop := c.serveHost(h)
	defer stop()
	resp, err := c.client.FeedAction(ctx, &pb.FeedActionRequest{
		HostServer: id, FeedUrl: req.FeedURL, Action: req.Action, Fields: req.Fields,
	})
	if err != nil {
		return FeedActionResult{}, err
	}
	if resp.Error != nil {
		return FeedActionResult{}, fromPBError(resp.Error)
	}
	r := resp.Result
	if r == nil {
		return FeedActionResult{}, nil
	}
	return FeedActionResult{Message: r.Message, Deleted: r.Deleted, Fields: fromPBFields(r.Fields)}, nil
}

var (
	_ Fetcher      = (*grpcFetcherClient)(nil)
	_ Renderer     = (*grpcFetcherClient)(nil)
	_ Docser       = (*grpcFetcherClient)(nil)
	_ SharedKeyer  = (*grpcFetcherClient)(nil)
	_ Enricher     = (*grpcFetcherClient)(nil)
	_ Decoration   = (*grpcFetcherClient)(nil)
	_ URLPolicy    = (*grpcFetcherClient)(nil)
	_ Provisioner  = (*grpcFetcherClient)(nil)
	_ FeedAdmin    = (*grpcFetcherClient)(nil)
	_ Configurable = (*grpcFetcherClient)(nil)
)

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
			Guid: it.GUID, Identity: it.Identity, SharedKey: it.SharedKey, Title: it.Title, Link: it.Link,
			Summary: it.Summary, ImageUrl: it.ImageURL, PublishedAt: it.PublishedAt,
			DurationSec: int32(it.DurationSec),
			Categories:  it.Categories,
			Enclosures:  encs,
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
			GUID: it.Guid, Identity: it.Identity, SharedKey: it.SharedKey, Title: it.Title, Link: it.Link,
			Summary: it.Summary, ImageURL: it.ImageUrl, PublishedAt: it.PublishedAt,
			DurationSec: int(it.DurationSec),
			Categories:  it.Categories,
			Enclosures:  encs,
		})
	}
	return out
}
