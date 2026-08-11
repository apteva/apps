package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type InstructorLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type InstructorStatistics struct {
	CoursesTaught     int `json:"courses_taught"`
	PublishedLessons  int `json:"published_lessons"`
	ActiveStudents    int `json:"active_students"`
	CompletedStudents int `json:"completed_students"`
}

type InstructorProfile struct {
	ID                  string                `json:"id"`
	CommunityID         string                `json:"community_id"`
	MemberID            *string               `json:"member_id,omitempty"`
	DisplayName         string                `json:"display_name"`
	AvatarStorageFileID *string               `json:"avatar_storage_file_id,omitempty"`
	ProfessionalTitle   string                `json:"professional_title"`
	SalesBio            string                `json:"sales_bio"`
	Credentials         []string              `json:"credentials"`
	Links               []InstructorLink      `json:"links"`
	Accomplishments     []string              `json:"accomplishments"`
	PublicVisible       bool                  `json:"public_visible"`
	ArchivedAt          *string               `json:"archived_at,omitempty"`
	CreatedAt           string                `json:"created_at"`
	UpdatedAt           string                `json:"updated_at"`
	Statistics          *InstructorStatistics `json:"statistics,omitempty"`
}

type PublicInstructorProfile struct {
	ID                  string               `json:"id"`
	DisplayName         string               `json:"display_name"`
	AvatarStorageFileID *string              `json:"avatar_storage_file_id,omitempty"`
	ProfessionalTitle   string               `json:"professional_title,omitempty"`
	SalesBio            string               `json:"sales_bio,omitempty"`
	Credentials         []string             `json:"credentials"`
	Links               []InstructorLink     `json:"links"`
	Accomplishments     []string             `json:"accomplishments"`
	Statistics          InstructorStatistics `json:"statistics"`
	Primary             bool                 `json:"primary"`
}

func instructorTools() []sdk.Tool {
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	links := map[string]any{"type": "array", "items": map[string]any{
		"type": "object", "properties": map[string]any{
			"label": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"},
		}, "required": []string{"url"},
	}}
	profileFields := map[string]any{
		"community_id":           map[string]any{"type": "string"},
		"member_id":              map[string]any{"type": "string"},
		"display_name":           map[string]any{"type": "string"},
		"avatar_storage_file_id": map[string]any{"type": "string"},
		"professional_title":     map[string]any{"type": "string"},
		"sales_bio":              map[string]any{"type": "string"},
		"credentials":            stringArray,
		"links":                  links,
		"accomplishments":        stringArray,
		"public_visible":         map[string]any{"type": "boolean"},
	}
	updateFields := map[string]any{"id": map[string]any{"type": "string"}}
	for key, value := range profileFields {
		if key != "community_id" {
			updateFields[key] = value
		}
	}
	return []sdk.Tool{
		{Name: "instructor_profiles_create", Description: "Create a reusable instructor profile in a community.", InputSchema: schemaObject(profileFields, []string{"community_id", "display_name"}), Handler: toolInstructorProfilesCreate},
		{Name: "instructor_profiles_update", Description: "Update an instructor profile's public biography, avatar, credentials, links, accomplishments, member link, or visibility.", InputSchema: schemaObject(updateFields, []string{"id"}), Handler: toolInstructorProfilesUpdate},
		{Name: "instructor_profiles_get", Description: "Fetch one instructor profile with calculated teaching statistics.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}), Handler: toolInstructorProfilesGet},
		{Name: "instructor_profiles_list", Description: "List instructor profiles in one community with calculated teaching statistics.", InputSchema: schemaObject(map[string]any{"community_id": map[string]any{"type": "string"}, "include_archived": map[string]any{"type": "boolean"}}, []string{"community_id"}), Handler: toolInstructorProfilesList},
		{Name: "instructor_profiles_archive", Description: "Archive an instructor profile. Set force=true to remove it from every assigned course first.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}}, []string{"id"}), Handler: toolInstructorProfilesArchive},
		{Name: "course_instructors_set", Description: "Set the ordered reusable instructor profiles and primary instructor for a course.", InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}, "instructor_ids": stringArray, "primary_instructor_id": map[string]any{"type": "string"}}, []string{"space_id", "instructor_ids"}), Handler: toolCourseInstructorsSet},
		{Name: "course_instructors_get", Description: "Get a course's ordered instructor profiles, primary instructor, calculated statistics, and legacy fallback name.", InputSchema: schemaObject(map[string]any{"space_id": map[string]any{"type": "string"}}, []string{"space_id"}), Handler: toolCourseInstructorsGet},
	}
}

func toolInstructorProfilesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), communityID)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityReadable(ctx, community); err != nil {
		return nil, err
	}
	profile := InstructorProfile{ID: newID("inst"), CommunityID: communityID, PublicVisible: true, Credentials: []string{}, Links: []InstructorLink{}, Accomplishments: []string{}}
	if err := applyInstructorArgs(ctx, &profile, args, true); err != nil {
		return nil, err
	}
	credentials, _ := json.Marshal(profile.Credentials)
	links, _ := json.Marshal(profile.Links)
	accomplishments, _ := json.Marshal(profile.Accomplishments)
	_, err = ctx.AppDB().Exec(`INSERT INTO instructor_profiles
		(id,community_id,member_id,display_name,avatar_storage_file_id,professional_title,sales_bio,
		 credentials_json,links_json,accomplishments_json,public_visible)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, profile.ID, profile.CommunityID, profile.MemberID, profile.DisplayName,
		profile.AvatarStorageFileID, profile.ProfessionalTitle, profile.SalesBio, string(credentials), string(links),
		string(accomplishments), boolInt(profile.PublicVisible))
	if err != nil {
		return nil, err
	}
	created, err := loadInstructorProfile(ctx.AppDB(), profile.ID)
	if err == nil {
		created.Statistics, _ = calculateInstructorStatistics(ctx.AppDB(), created.CommunityID, created.ID)
		emit(ctx, "instructor.created", map[string]any{"community_id": created.CommunityID, "instructor_id": created.ID})
	}
	return created, err
}

func toolInstructorProfilesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	profile, err := loadInstructorProfile(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), profile.CommunityID)
	if err != nil || ensureCommunityReadable(ctx, community) != nil {
		return nil, fmt.Errorf("instructor profile not found")
	}
	if err := applyInstructorArgs(ctx, &profile, args, false); err != nil {
		return nil, err
	}
	credentials, _ := json.Marshal(profile.Credentials)
	links, _ := json.Marshal(profile.Links)
	accomplishments, _ := json.Marshal(profile.Accomplishments)
	_, err = ctx.AppDB().Exec(`UPDATE instructor_profiles SET member_id=?,display_name=?,avatar_storage_file_id=?,
		professional_title=?,sales_bio=?,credentials_json=?,links_json=?,accomplishments_json=?,public_visible=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		profile.MemberID, profile.DisplayName, profile.AvatarStorageFileID, profile.ProfessionalTitle, profile.SalesBio,
		string(credentials), string(links), string(accomplishments), boolInt(profile.PublicVisible), profile.ID)
	if err != nil {
		return nil, err
	}
	updated, err := loadInstructorProfile(ctx.AppDB(), id)
	if err == nil {
		updated.Statistics, _ = calculateInstructorStatistics(ctx.AppDB(), updated.CommunityID, updated.ID)
		emit(ctx, "instructor.updated", map[string]any{"community_id": updated.CommunityID, "instructor_id": updated.ID})
	}
	return updated, err
}

func toolInstructorProfilesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	profile, err := loadInstructorProfile(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), profile.CommunityID)
	if err != nil || ensureCommunityReadable(ctx, community) != nil {
		return nil, fmt.Errorf("instructor profile not found")
	}
	profile.Statistics, _ = calculateInstructorStatistics(ctx.AppDB(), profile.CommunityID, profile.ID)
	return profile, nil
}

func toolInstructorProfilesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), communityID)
	if err != nil || ensureCommunityReadable(ctx, community) != nil {
		return nil, fmt.Errorf("community not found")
	}
	query := `SELECT ` + instructorProfileCols + ` FROM instructor_profiles i WHERE i.community_id=?`
	if include, _ := args["include_archived"].(bool); !include {
		query += ` AND i.archived_at IS NULL`
	}
	query += ` ORDER BY i.display_name,i.created_at`
	rows, err := ctx.AppDB().Query(query, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstructorProfile{}
	for rows.Next() {
		profile, err := scanInstructorProfile(rows.Scan)
		if err != nil {
			return nil, err
		}
		profile.Statistics, _ = calculateInstructorStatistics(ctx.AppDB(), profile.CommunityID, profile.ID)
		out = append(out, profile)
	}
	return map[string]any{"instructors": out, "count": len(out)}, rows.Err()
}

func toolInstructorProfilesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	profile, err := loadInstructorProfile(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), profile.CommunityID)
	if err != nil || ensureCommunityReadable(ctx, community) != nil {
		return nil, fmt.Errorf("instructor profile not found")
	}
	references, err := instructorCourseReferences(ctx.AppDB(), profile.CommunityID, id)
	if err != nil {
		return nil, err
	}
	force, _ := args["force"].(bool)
	if len(references) > 0 && !force {
		return nil, fmt.Errorf("instructor is assigned to %d courses; use force to remove those assignments", len(references))
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if force {
		for _, reference := range references {
			if err := removeInstructorFromCourse(tx, reference, id); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE instructor_profiles SET archived_at=CURRENT_TIMESTAMP,public_visible=0,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, reference := range references {
		emit(ctx, "course.instructors_updated", map[string]any{
			"community_id": profile.CommunityID,
			"space_id":     reference,
		})
	}
	emit(ctx, "instructor.archived", map[string]any{"community_id": profile.CommunityID, "instructor_id": id})
	return map[string]any{"archived": true, "id": id, "courses_updated": len(references)}, nil
}

func toolCourseInstructorsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	space, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	ids, ok := stringArrayArg(args, "instructor_ids")
	if !ok {
		return nil, errors.New("instructor_ids is required")
	}
	if len(ids) > 20 {
		return nil, errors.New("a course can have at most 20 instructors")
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return nil, errors.New("instructor_ids must contain unique non-empty ids")
		}
		profile, err := loadInstructorProfile(ctx.AppDB(), id)
		if err != nil || profile.CommunityID != space.CommunityID || profile.ArchivedAt != nil {
			return nil, fmt.Errorf("instructor %q is not active in this course's community", id)
		}
		seen[id] = true
		clean = append(clean, id)
	}
	primary := strings.TrimSpace(strArg(args, "primary_instructor_id", ""))
	if len(clean) == 0 {
		primary = ""
	} else if primary == "" {
		primary = clean[0]
	} else if !seen[primary] {
		return nil, errors.New("primary_instructor_id must appear in instructor_ids")
	}
	encoded, _ := json.Marshal(clean)
	var primaryValue any
	if primary != "" {
		primaryValue = primary
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO course_details(space_id,instructor_ids_json,primary_instructor_id)
		VALUES(?,?,?) ON CONFLICT(space_id) DO UPDATE SET instructor_ids_json=excluded.instructor_ids_json,
		primary_instructor_id=excluded.primary_instructor_id,updated_at=CURRENT_TIMESTAMP`, spaceID, string(encoded), primaryValue)
	if err != nil {
		return nil, err
	}
	emit(ctx, "course.instructors_updated", map[string]any{"community_id": space.CommunityID, "space_id": spaceID, "instructor_ids": clean})
	return courseInstructorsResult(ctx.AppDB(), spaceID)
}

func toolCourseInstructorsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if _, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	return courseInstructorsResult(ctx.AppDB(), spaceID)
}

func courseInstructorsResult(db *sql.DB, spaceID string) (map[string]any, error) {
	details, err := loadCourseDetails(db, spaceID)
	if err != nil {
		return nil, err
	}
	profiles := []InstructorProfile{}
	for _, id := range details.InstructorIDs {
		profile, err := loadInstructorProfile(db, id)
		if err != nil || profile.ArchivedAt != nil {
			continue
		}
		profile.Statistics, _ = calculateInstructorStatistics(db, profile.CommunityID, profile.ID)
		profiles = append(profiles, profile)
	}
	return map[string]any{
		"space_id": spaceID, "instructors": profiles, "instructor_ids": details.InstructorIDs,
		"primary_instructor_id": details.PrimaryInstructorID, "legacy_instructor_name": details.InstructorName,
	}, nil
}

func applyInstructorArgs(ctx *sdk.AppCtx, profile *InstructorProfile, args map[string]any, creating bool) error {
	if value, ok := args["display_name"].(string); ok {
		profile.DisplayName = strings.TrimSpace(value)
	}
	if profile.DisplayName == "" {
		return errors.New("display_name is required")
	}
	if value, ok := args["member_id"].(string); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			profile.MemberID = nil
		} else {
			if err := verifyMember(ctx.AppDB(), profile.CommunityID, value); err != nil {
				return err
			}
			profile.MemberID = &value
		}
	}
	if value, ok := args["avatar_storage_file_id"].(string); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			profile.AvatarStorageFileID = nil
		} else {
			if err := validateStorageFile(ctx, value); err != nil {
				return err
			}
			profile.AvatarStorageFileID = &value
		}
	}
	if value, ok := args["professional_title"].(string); ok {
		profile.ProfessionalTitle = strings.TrimSpace(value)
	}
	if value, ok := args["sales_bio"].(string); ok {
		profile.SalesBio = strings.TrimSpace(value)
	}
	if value, ok := stringArrayArg(args, "credentials"); ok {
		profile.Credentials = cleanStringList(value)
	}
	if value, ok := stringArrayArg(args, "accomplishments"); ok {
		profile.Accomplishments = cleanStringList(value)
	}
	if _, ok := args["links"]; ok {
		links, err := instructorLinksArg(args["links"])
		if err != nil {
			return err
		}
		profile.Links = links
	}
	if value, ok := args["public_visible"].(bool); ok {
		profile.PublicVisible = value
	} else if creating {
		profile.PublicVisible = true
	}
	return nil
}

func instructorLinksArg(raw any) ([]InstructorLink, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("links must be an array")
	}
	var links []InstructorLink
	if err := json.Unmarshal(encoded, &links); err != nil {
		return nil, errors.New("links must contain label and url fields")
	}
	for index := range links {
		links[index].Label = strings.TrimSpace(links[index].Label)
		links[index].URL = strings.TrimSpace(links[index].URL)
		parsed, err := url.Parse(links[index].URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("link %d must be an absolute http(s) URL", index+1)
		}
	}
	return links, nil
}

func cleanStringList(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

const instructorProfileCols = `i.id,i.community_id,i.member_id,i.display_name,i.avatar_storage_file_id,
	i.professional_title,i.sales_bio,i.credentials_json,i.links_json,i.accomplishments_json,
	i.public_visible,i.archived_at,i.created_at,i.updated_at`

func loadInstructorProfile(db *sql.DB, id string) (InstructorProfile, error) {
	profile, err := scanInstructorProfile(db.QueryRow(`SELECT `+instructorProfileCols+` FROM instructor_profiles i WHERE i.id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, fmt.Errorf("instructor profile not found")
	}
	return profile, err
}

func scanInstructorProfile(scan func(...any) error) (InstructorProfile, error) {
	profile := InstructorProfile{Credentials: []string{}, Links: []InstructorLink{}, Accomplishments: []string{}}
	var memberID, avatar, archived sql.NullString
	var credentialsJSON, linksJSON, accomplishmentsJSON string
	var publicVisible int
	if err := scan(&profile.ID, &profile.CommunityID, &memberID, &profile.DisplayName, &avatar,
		&profile.ProfessionalTitle, &profile.SalesBio, &credentialsJSON, &linksJSON, &accomplishmentsJSON,
		&publicVisible, &archived, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		return profile, err
	}
	if memberID.Valid {
		profile.MemberID = &memberID.String
	}
	if avatar.Valid {
		profile.AvatarStorageFileID = &avatar.String
	}
	if archived.Valid {
		profile.ArchivedAt = &archived.String
	}
	profile.PublicVisible = publicVisible != 0
	_ = json.Unmarshal([]byte(credentialsJSON), &profile.Credentials)
	_ = json.Unmarshal([]byte(linksJSON), &profile.Links)
	_ = json.Unmarshal([]byte(accomplishmentsJSON), &profile.Accomplishments)
	return profile, nil
}

func calculateInstructorStatistics(db *sql.DB, communityID, instructorID string) (*InstructorStatistics, error) {
	rows, err := db.Query(`SELECT cd.space_id,cd.instructor_ids_json FROM course_details cd
		JOIN spaces s ON s.id=cd.space_id WHERE s.community_id=? AND s.kind='course' AND s.archived_at IS NULL`, communityID)
	if err != nil {
		return nil, err
	}
	courseIDs := []string{}
	for rows.Next() {
		var spaceID, raw string
		if err := rows.Scan(&spaceID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var ids []string
		_ = json.Unmarshal([]byte(raw), &ids)
		for _, id := range ids {
			if id == instructorID {
				courseIDs = append(courseIDs, spaceID)
				break
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	stats := &InstructorStatistics{CoursesTaught: len(courseIDs)}
	if len(courseIDs) == 0 {
		return stats, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(courseIDs)), ",")
	args := make([]any, len(courseIDs))
	for index := range courseIDs {
		args[index] = courseIDs[index]
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lessons l JOIN sections s ON s.id=l.section_id
		WHERE l.published_at IS NOT NULL AND s.space_id IN (`+placeholders+`)`, args...).Scan(&stats.PublishedLessons); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(DISTINCT member_id) FROM course_enrollments
		WHERE status='active' AND access_revoked_at IS NULL AND space_id IN (`+placeholders+`)`, args...).Scan(&stats.ActiveStudents); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(DISTINCT member_id) FROM course_enrollments
		WHERE status='completed' AND space_id IN (`+placeholders+`)`, args...).Scan(&stats.CompletedStudents); err != nil {
		return nil, err
	}
	return stats, nil
}

func publicInstructorsForCourse(db *sql.DB, spaceID string) ([]PublicInstructorProfile, error) {
	details, err := loadCourseDetails(db, spaceID)
	if err != nil {
		return nil, err
	}
	out := []PublicInstructorProfile{}
	for _, id := range details.InstructorIDs {
		profile, err := loadInstructorProfile(db, id)
		if err != nil || profile.ArchivedAt != nil || !profile.PublicVisible {
			continue
		}
		stats, _ := calculateInstructorStatistics(db, profile.CommunityID, profile.ID)
		if stats == nil {
			stats = &InstructorStatistics{}
		}
		out = append(out, PublicInstructorProfile{
			ID: profile.ID, DisplayName: profile.DisplayName, AvatarStorageFileID: profile.AvatarStorageFileID,
			ProfessionalTitle: profile.ProfessionalTitle, SalesBio: profile.SalesBio,
			Credentials: append([]string{}, profile.Credentials...), Links: append([]InstructorLink{}, profile.Links...),
			Accomplishments: append([]string{}, profile.Accomplishments...), Statistics: *stats,
			Primary: details.PrimaryInstructorID != nil && *details.PrimaryInstructorID == profile.ID,
		})
	}
	if len(out) == 0 && strings.TrimSpace(details.InstructorName) != "" {
		out = append(out, PublicInstructorProfile{
			ID: "legacy:" + spaceID, DisplayName: strings.TrimSpace(details.InstructorName), Credentials: []string{},
			Links: []InstructorLink{}, Accomplishments: []string{}, Primary: true,
		})
	}
	return out, nil
}

func instructorCourseReferences(db *sql.DB, communityID, instructorID string) ([]string, error) {
	rows, err := db.Query(`SELECT cd.space_id,cd.instructor_ids_json FROM course_details cd JOIN spaces s ON s.id=cd.space_id WHERE s.community_id=?`, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var spaceID, raw string
		if err := rows.Scan(&spaceID, &raw); err != nil {
			return nil, err
		}
		var ids []string
		_ = json.Unmarshal([]byte(raw), &ids)
		for _, id := range ids {
			if id == instructorID {
				out = append(out, spaceID)
				break
			}
		}
	}
	return out, rows.Err()
}

func removeInstructorFromCourse(tx *sql.Tx, spaceID, instructorID string) error {
	var raw string
	var currentPrimary sql.NullString
	if err := tx.QueryRow(`SELECT instructor_ids_json,primary_instructor_id FROM course_details WHERE space_id=?`, spaceID).Scan(&raw, &currentPrimary); err != nil {
		return err
	}
	currentIDs := []string{}
	_ = json.Unmarshal([]byte(raw), &currentIDs)
	ids := []string{}
	for _, id := range currentIDs {
		if id != instructorID {
			ids = append(ids, id)
		}
	}
	primary := ""
	if currentPrimary.Valid && currentPrimary.String != instructorID {
		primary = currentPrimary.String
	} else if len(ids) > 0 {
		primary = ids[0]
	}
	encoded, _ := json.Marshal(ids)
	var primaryValue any
	if primary != "" {
		primaryValue = primary
	}
	_, err := tx.Exec(`UPDATE course_details SET instructor_ids_json=?,primary_instructor_id=?,updated_at=CURRENT_TIMESTAMP WHERE space_id=?`, string(encoded), primaryValue, spaceID)
	return err
}
