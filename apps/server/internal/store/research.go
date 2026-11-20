package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// The creator research engine's tables (migration 00027, D-41). They are shared across workspaces, so no function here takes a workspace,
// and only platform code (jobs, cfctl) calls them. Every write is checked by package research first and again by the database.

// The rows of the research tables.
type (
	ResearchCreator    = db.ResearchCreator
	ResearchPost       = db.ResearchPost
	ResearchEvidence   = db.ResearchEvidence
	ResearchAnnotation = db.ResearchAnnotation
	ResearchFinding    = db.ResearchFinding
)

// The statuses of a research creator.
const (
	ResearchCandidate = "candidate"
	ResearchApproved  = "approved"
	ResearchDropped   = "dropped"
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// NewResearchCreator is a creator to add or refresh.
type NewResearchCreator struct {
	Platform research.Platform
	Handle   string
	// WhySimilar is our own words, at most 500 characters. Similarity is 0 to 1.
	WhySimilar string
	Similarity float64
}

// UpsertResearchCreator adds a creator as a candidate, or refreshes the words about one already known (their status stays). The profile
// link is built from the platform and the handle, never taken from input.
func (s *Store) UpsertResearchCreator(ctx context.Context, in NewResearchCreator) (ResearchCreator, error) {
	if _, err := research.ParsePlatform(string(in.Platform)); err != nil {
		return ResearchCreator{}, invalidf("%v", err)
	}
	handle, err := research.NormalizeHandle(in.Handle)
	if err != nil {
		return ResearchCreator{}, invalidf("%v", err)
	}
	link, err := research.ProfileURL(in.Platform, handle)
	if err != nil {
		return ResearchCreator{}, invalidf("%v", err)
	}
	r, err := s.q.UpsertResearchCreator(ctx, db.UpsertResearchCreatorParams{
		Platform: string(in.Platform), Handle: handle, ProfileUrl: link, WhySimilar: in.WhySimilar, Similarity: in.Similarity,
	})
	return r, mapErr(err)
}

// GetResearchCreator returns a creator, or ErrNotFound.
func (s *Store) GetResearchCreator(ctx context.Context, p research.Platform, handle string) (ResearchCreator, error) {
	h, err := research.NormalizeHandle(handle)
	if err != nil {
		return ResearchCreator{}, invalidf("%v", err)
	}
	r, err := s.q.GetResearchCreator(ctx, db.GetResearchCreatorParams{Platform: string(p), Handle: h})
	return r, mapErr(err)
}

// ListResearchCreators lists the creators with a status ("" for all of them).
func (s *Store) ListResearchCreators(ctx context.Context, status string) ([]ResearchCreator, error) {
	rows, err := s.q.ListResearchCreators(ctx, status)
	return rows, mapErr(err)
}

// SetResearchCreatorStatus moves a creator between candidate, approved and dropped. Approving needs the name of the person who approved
// (a creator is sampled only after a person approved them).
func (s *Store) SetResearchCreatorStatus(ctx context.Context, id uuid.UUID, status, approvedBy string) (ResearchCreator, error) {
	if !slices.Contains([]string{ResearchCandidate, ResearchApproved, ResearchDropped}, status) {
		return ResearchCreator{}, invalidf("research creator status %q", status)
	}
	if status == ResearchApproved && strings.TrimSpace(approvedBy) == "" {
		return ResearchCreator{}, invalidf("approving a creator needs the name of the person who approved")
	}
	r, err := s.q.SetResearchCreatorStatus(ctx, db.SetResearchCreatorStatusParams{ID: id, Status: status, ApprovedBy: strings.TrimSpace(approvedBy)})
	return r, mapErr(err)
}

// NewResearchPost is a post to add or refresh. The link must be a post page (research.ParsePostURL).
type NewResearchPost struct {
	URL       string
	CreatorID uuid.UUID
	// PostedAt and PostedAtSource (id, api or manual) are set together or both empty.
	PostedAt       *time.Time
	PostedAtSource string
	DurationS      *float64
}

// UpsertResearchPost stores the facts about one post page. A link that does not point to a post page (a profile, a media file, another
// site) is refused, and a post already stored under another creator is a conflict.
func (s *Store) UpsertResearchPost(ctx context.Context, in NewResearchPost) (ResearchPost, error) {
	ref, err := research.ParsePostURL(in.URL)
	if err != nil {
		return ResearchPost{}, invalidf("%v", err)
	}
	if err := research.CheckPostTime(in.PostedAt, in.PostedAtSource, time.Now()); err != nil {
		return ResearchPost{}, invalidf("%v", err)
	}
	if in.DurationS != nil && (*in.DurationS < 0 || *in.DurationS > 7200) {
		return ResearchPost{}, invalidf("duration_s must be between 0 and 7200")
	}
	var src *string
	if in.PostedAtSource != "" {
		src = &in.PostedAtSource
	}
	r, err := s.q.UpsertResearchPost(ctx, db.UpsertResearchPostParams{
		Platform: string(ref.Platform), Url: ref.URL, ExternalID: ref.ExternalID, CreatorID: in.CreatorID,
		PostedAt: in.PostedAt, PostedAtSource: src, DurationS: in.DurationS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The WHERE of the upsert did not match: the post exists under another creator.
		return ResearchPost{}, fmt.Errorf("%w: the post is stored under another creator", ErrConflict)
	}
	return r, mapErr(err)
}

// GetResearchPostByURL returns the post stored for a post link, or ErrNotFound.
func (s *Store) GetResearchPostByURL(ctx context.Context, link string) (ResearchPost, error) {
	ref, err := research.ParsePostURL(link)
	if err != nil {
		return ResearchPost{}, invalidf("%v", err)
	}
	r, err := s.q.GetResearchPostByURL(ctx, ref.URL)
	return r, mapErr(err)
}

// NewResearchEvidence is one observation. Exactly one of PostID and CreatorID is set.
type NewResearchEvidence struct {
	PostID    uuid.UUID
	CreatorID uuid.UUID
	Source    research.Source
	Method    research.Method
	// ObservedAt is when it was seen; zero means now.
	ObservedAt time.Time
	// Observer is a person or a run ("sam", "run:2026-47").
	Observer string
	// Metrics are only the counts the source gives (research.Source.Metrics).
	Metrics map[string]float64
	// RawRef is the hex SHA-256 of the response the observation was read from, "" for none. Never the response.
	RawRef string
	// Retention decides retain_until; the zero value is the default (12 months, 30 days for YouTube).
	Retention research.Retention
	RunID     uuid.UUID
}

// AddResearchEvidence stores one observation. It refuses a source that is not on the allow-list, a method the source cannot use, a
// platform the source cannot see, a metric the source does not give and anything but numbers. retain_until is set from the retention.
func (s *Store) AddResearchEvidence(ctx context.Context, in NewResearchEvidence) (ResearchEvidence, error) {
	if (in.PostID == uuid.Nil) == (in.CreatorID == uuid.Nil) {
		return ResearchEvidence{}, invalidf("an observation is about a post or a creator: exactly one of them")
	}
	if _, err := research.ParseSource(string(in.Source)); err != nil {
		return ResearchEvidence{}, invalidf("%v", err)
	}
	if _, err := research.ParseMethod(string(in.Method)); err != nil {
		return ResearchEvidence{}, invalidf("%v", err)
	}
	var platform string
	var err error
	if in.PostID != uuid.Nil {
		platform, err = s.q.GetResearchPostPlatform(ctx, in.PostID)
	} else {
		platform, err = s.q.GetResearchCreatorPlatform(ctx, in.CreatorID)
	}
	if err != nil {
		return ResearchEvidence{}, mapErr(err)
	}
	if err := research.CheckObservation(in.Source, in.Method, research.Platform(platform), in.Metrics); err != nil {
		return ResearchEvidence{}, invalidf("%v", err)
	}
	ret := in.Retention
	if ret == (research.Retention{}) {
		ret = research.DefaultRetention()
	}
	if err := ret.Valid(); err != nil {
		return ResearchEvidence{}, invalidf("%v", err)
	}
	observed := in.ObservedAt
	if observed.IsZero() {
		observed = time.Now()
	}
	observed = observed.UTC().Truncate(time.Microsecond)
	metrics := json.RawMessage(`{}`)
	if len(in.Metrics) > 0 {
		if metrics, err = json.Marshal(in.Metrics); err != nil {
			return ResearchEvidence{}, invalidf("metrics: %v", err)
		}
	}
	var raw *string
	if in.RawRef != "" {
		raw = &in.RawRef
	}
	r, err := s.q.InsertResearchEvidence(ctx, db.InsertResearchEvidenceParams{
		PostID: nullUUID(in.PostID), CreatorID: nullUUID(in.CreatorID), Source: string(in.Source), Method: string(in.Method),
		ObservedAt: observed, Observer: strings.TrimSpace(in.Observer), Metrics: metrics, RawRef: raw,
		RetainUntil: ret.RetainUntil(in.Source, observed), RunID: nullUUID(in.RunID),
	})
	return r, mapErr(err)
}

func nullUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// ListResearchEvidenceForPost lists a post's observations, oldest first.
func (s *Store) ListResearchEvidenceForPost(ctx context.Context, postID uuid.UUID) ([]ResearchEvidence, error) {
	rows, err := s.q.ListResearchEvidenceForPost(ctx, nullUUID(postID))
	return rows, mapErr(err)
}

// SaveResearchAnnotation stores (or replaces, per post and annotator) what was noted about a post, after checking it against the
// vocabulary (research.Vocabulary.CheckAnnotation): the format must be a card of the library, the topic a value of the tag vocabulary,
// and the hook text a title form, never the creator's words.
func (s *Store) SaveResearchAnnotation(ctx context.Context, postID uuid.UUID, a research.Annotation) (ResearchAnnotation, error) {
	if err := research.DefaultVocabulary().CheckAnnotation(a); err != nil {
		return ResearchAnnotation{}, invalidf("%v", err)
	}
	str := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	i32 := func(v *int) *int32 {
		if v == nil {
			return nil
		}
		n := int32(*v) //nolint:gosec // checked by the column's range
		return &n
	}
	r, err := s.q.UpsertResearchAnnotation(ctx, db.UpsertResearchAnnotationParams{
		PostID: postID, FormatID: str(a.FormatID), HookKind: str(a.HookKind), HookTextPattern: str(a.HookTextPattern),
		TextPattern: str(a.TextPattern), CutsEstimate: i32(a.CutsEstimate), FirstCutS: a.FirstCutS, CaptionLen: i32(a.CaptionLen),
		HashtagsN: i32(a.HashtagsN), Cta: str(a.CTA), Topic: str(a.Topic), Annotator: strings.TrimSpace(a.Annotator), Confidence: a.Confidence,
	})
	return r, mapErr(err)
}

// ListResearchAnnotationsForPost lists the annotations of a post.
func (s *Store) ListResearchAnnotationsForPost(ctx context.Context, postID uuid.UUID) ([]ResearchAnnotation, error) {
	rows, err := s.q.ListResearchAnnotationsForPost(ctx, postID)
	return rows, mapErr(err)
}

// NewResearchFinding is a claim of the playbook.
type NewResearchFinding struct {
	PlaybookVersion string
	Claim           string
	Dimension       string
	EffectValue     float64
	EffectUnit      string
	NPosts          int
	NCreators       int
	Confidence      string
	// EvidenceIDs are the observations the claim rests on: at least one.
	EvidenceIDs []uuid.UUID
}

// AddResearchFinding stores a finding. A claim without evidence is refused.
func (s *Store) AddResearchFinding(ctx context.Context, in NewResearchFinding) (ResearchFinding, error) {
	if len(in.EvidenceIDs) == 0 {
		return ResearchFinding{}, invalidf("a finding needs the evidence it rests on")
	}
	if !slices.Contains(research.Dimensions(), in.Dimension) {
		return ResearchFinding{}, invalidf("dimension %q is not one of %v", in.Dimension, research.Dimensions())
	}
	if !slices.Contains(research.Confidences(), in.Confidence) {
		return ResearchFinding{}, invalidf("confidence %q is not one of %v", in.Confidence, research.Confidences())
	}
	r, err := s.q.InsertResearchFinding(ctx, db.InsertResearchFindingParams{
		PlaybookVersion: in.PlaybookVersion, Claim: in.Claim, Dimension: in.Dimension, EffectValue: in.EffectValue, EffectUnit: in.EffectUnit,
		NPosts: int32(in.NPosts), NCreators: int32(in.NCreators), Confidence: in.Confidence, EvidenceIds: in.EvidenceIDs, //nolint:gosec // small
	})
	return r, mapErr(err)
}

// ListResearchFindings lists the findings of one playbook version.
func (s *Store) ListResearchFindings(ctx context.Context, playbookVersion string) ([]ResearchFinding, error) {
	rows, err := s.q.ListResearchFindings(ctx, playbookVersion)
	return rows, mapErr(err)
}

// ResearchOrphanGrace is how long a post waits for its first observation before the sweep treats it as an orphan.
const ResearchOrphanGrace = 24 * time.Hour

// ResearchSweepResult is what one sweep deleted.
type ResearchSweepResult struct {
	Evidence int64
	Findings int64
	Posts    int64
	Creators int64
}

// SweepResearch is the retention sweeper: it deletes every observation whose retain_until has passed, the findings that rested on one
// (the playbook is regenerated from what is left), the posts left without any observation (and with them their annotations) and the
// candidate or dropped creators left with nothing and untouched for idle. It runs in one transaction and is safe to run twice. A
// creator that was approved stays: a person chose them, and only `forget` removes them.
func (s *Store) SweepResearch(ctx context.Context, now time.Time, idle time.Duration) (ResearchSweepResult, error) {
	var res ResearchSweepResult
	err := s.runTx(ctx, func(tx *Store) error {
		expired, err := tx.q.DeleteExpiredResearchEvidence(ctx, now)
		if err != nil {
			return err
		}
		res.Evidence = int64(len(expired))
		if len(expired) > 0 {
			if res.Findings, err = tx.q.DeleteResearchFindingsByEvidence(ctx, expired); err != nil {
				return err
			}
		}
		if res.Posts, err = tx.q.DeleteOrphanResearchPosts(ctx, now.Add(-ResearchOrphanGrace)); err != nil {
			return err
		}
		res.Creators, err = tx.q.DeleteIdleResearchCreators(ctx, now.Add(-idle))
		return err
	})
	return res, mapErr(err)
}

// ResearchForgetResult is what a takedown deleted.
type ResearchForgetResult struct {
	Creators    int
	Posts       int64
	Annotations int64
	Evidence    int64
	Findings    int64
}

// ForgetResearchCreator is the takedown: it deletes the creator with the handle (on one platform, or on every platform when platform is
// empty), their posts, annotations and evidence, and the findings that rested on that evidence. It returns ErrNotFound when no creator has
// the handle. Run it on request and say so in the log: it cannot be undone.
func (s *Store) ForgetResearchCreator(ctx context.Context, platform research.Platform, handle string) (ResearchForgetResult, error) {
	h, err := research.NormalizeHandle(handle)
	if err != nil {
		return ResearchForgetResult{}, invalidf("%v", err)
	}
	if platform != "" {
		if _, err := research.ParsePlatform(string(platform)); err != nil {
			return ResearchForgetResult{}, invalidf("%v", err)
		}
	}
	var res ResearchForgetResult
	err = s.runTx(ctx, func(tx *Store) error {
		creators, err := tx.q.ListResearchCreatorsByHandle(ctx, db.ListResearchCreatorsByHandleParams{Handle: h, Platform: string(platform)})
		if err != nil {
			return err
		}
		if len(creators) == 0 {
			return ErrNotFound
		}
		for _, c := range creators {
			counts, err := tx.q.CountResearchOfCreator(ctx, c.ID)
			if err != nil {
				return err
			}
			evidence, err := tx.q.ListResearchEvidenceIDsOfCreator(ctx, nullUUID(c.ID))
			if err != nil {
				return err
			}
			if len(evidence) > 0 {
				n, err := tx.q.DeleteResearchFindingsByEvidence(ctx, evidence)
				if err != nil {
					return err
				}
				res.Findings += n
			}
			if _, err := tx.q.DeleteResearchCreator(ctx, c.ID); err != nil {
				return err
			}
			res.Creators++
			res.Posts += counts.Posts
			res.Annotations += counts.Annotations
			res.Evidence += counts.Evidence
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ResearchForgetResult{}, ErrNotFound
	}
	return res, mapErr(err)
}

// runTx runs fn in a transaction, or directly when the Store already is one.
func (s *Store) runTx(ctx context.Context, fn func(tx *Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	return s.InTx(ctx, fn)
}
