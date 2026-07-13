package main

import (
	"errors"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolTestimonialsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := testimonialFromArgs(args)
	if err != nil {
		return nil, err
	}
	created, err := createTestimonial(ctx.AppDB(), ctx.CurrentProject(), &t)
	if err != nil {
		return nil, err
	}
	ctx.Emit("testimonial.created", map[string]any{"id": created.ID, "status": created.Status, "kind": created.Kind})
	return map[string]any{"created": true, "testimonial": created}, nil
}

func (a *App) toolTestimonialsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit, _ := intArg(args, "limit")
	offset, _ := intArg(args, "offset")
	publishedOnly := boolArg(args, "published_only")
	items, total, err := listTestimonialsPage(ctx.AppDB(), ctx.CurrentProject(), TestimonialFilter{
		Status:          strArg(args, "status"),
		Kind:            strArg(args, "kind"),
		Source:          strArg(args, "source"),
		Tag:             strArg(args, "tag"),
		Q:               strArg(args, "q"),
		PublishedOnly:   publishedOnly,
		IncludeArchived: boolArg(args, "include_archived"),
		Limit:           limit,
		Offset:          offset,
	})
	if err != nil {
		return nil, err
	}
	var responseItems any = items
	if publishedOnly {
		responseItems = publicTestimonials(items)
	}
	return map[string]any{
		"testimonials": responseItems,
		"count":        len(items),
		"total":        total,
		"limit":        normalizedLimit(limit),
		"offset":       offset,
		"has_more":     offset+len(items) < total,
	}, nil
}

func (a *App) toolTestimonialsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	item, err := getTestimonial(ctx.AppDB(), ctx.CurrentProject(), id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return map[string]any{"found": false}, nil
	}
	return map[string]any{"found": true, "testimonial": item}, nil
}

func (a *App) toolTestimonialsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	before, err := getTestimonial(ctx.AppDB(), ctx.CurrentProject(), id)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return map[string]any{"found": false}, nil
	}
	item, err := updateTestimonial(ctx.AppDB(), ctx.CurrentProject(), id, args)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	ctx.Emit("testimonial.updated", map[string]any{"id": item.ID, "status": item.Status, "kind": item.Kind})
	if before.Status != item.Status {
		ctx.Emit("testimonial.status_changed", map[string]any{"id": item.ID, "status": item.Status})
	}
	return map[string]any{"updated": true, "testimonial": item}, nil
}

func (a *App) toolTestimonialsSetStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	status := strArg(args, "status")
	item, err := setTestimonialStatus(ctx.AppDB(), ctx.CurrentProject(), id, status)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	ctx.Emit("testimonial.status_changed", map[string]any{"id": item.ID, "status": item.Status})
	return map[string]any{"updated": true, "testimonial": item}, nil
}

func (a *App) toolTestimonialsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	hard := boolArg(args, "hard")
	if err := deleteTestimonial(ctx.AppDB(), ctx.CurrentProject(), id, hard); err != nil {
		if errors.Is(err, errNotFound) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	ctx.Emit("testimonial.deleted", map[string]any{"id": id, "hard": hard})
	if !hard {
		ctx.Emit("testimonial.status_changed", map[string]any{"id": id, "status": "archived"})
	}
	return map[string]any{"deleted": hard, "archived": !hard, "id": id}, nil
}
