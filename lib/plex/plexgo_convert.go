package plex

import (
	"context"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/LukeHagar/plexgo/models/operations"
)

func float32PtrToFloat64Ptr(p *float32) *float64 {
	if p == nil {
		return nil
	}
	x := float64(*p)
	return &x
}

func metadataToPlexItem(md *components.Metadata) PlexItem {
	if md == nil {
		return PlexItem{}
	}
	ratingKey := ""
	if md.RatingKey != nil {
		ratingKey = *md.RatingKey
	}
	summary := ""
	if md.Summary != nil {
		summary = *md.Summary
	}
	return PlexItem{
		RatingKey:  ratingKey,
		Key:        md.Key,
		Title:      md.Title,
		Type:       md.Type,
		Year:       md.Year,
		Rating:     float32PtrToFloat64Ptr(md.Rating),
		Summary:    summary,
		Thumb:      md.Thumb,
		Art:        md.Art,
		Duration:   md.Duration,
		AddedAt:    md.AddedAt,
		UpdatedAt:  md.UpdatedAt,
		ViewCount:  md.ViewCount,
		Genre:      md.Genre,
		LeafCount:  md.LeafCount,
		ChildCount: md.ChildCount,
	}
}

// listSectionContentAll pages plexgo Content.ListContent (GET …/library/sections/{id}/all).
func (c *Client) listSectionContentAll(ctx context.Context, sectionID string) ([]PlexItem, error) {
	const pageSize = 200
	start := 0
	var all []PlexItem
	for range 500 {
		startPtr, sizePtr := start, pageSize
		resp, err := c.api.Content.ListContent(ctx, operations.ListContentRequest{
			SectionID:             sectionID,
			XPlexContainerStart:   &startPtr,
			XPlexContainerSize:    &sizePtr,
		})
		if err != nil {
			return nil, err
		}
		if resp == nil || resp.MediaContainerWithMetadata == nil || resp.MediaContainerWithMetadata.MediaContainer == nil {
			break
		}
		mdList := resp.MediaContainerWithMetadata.MediaContainer.Metadata
		if len(mdList) == 0 {
			break
		}
		for i := range mdList {
			all = append(all, metadataToPlexItem(&mdList[i]))
		}
		n := len(mdList)
		var total int64
		if ts := resp.MediaContainerWithMetadata.MediaContainer.TotalSize; ts != nil {
			total = *ts
		}
		start += n
		if total > 0 && int64(start) >= total {
			break
		}
		if n < pageSize {
			break
		}
	}
	return all, nil
}
