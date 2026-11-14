#!/usr/bin/env python3
"""Seed ordinary Social rows in an existing local development install only."""

import argparse
import datetime as dt
import json
from pathlib import Path
import sqlite3


SOURCE = "local-performance-v1"
BRANDS = (
    ("Aurora Studio", "aurora-studio", "#6366f1"),
    ("Cedar Outdoors", "cedar-outdoors", "#16a34a"),
    ("Ember Kitchen", "ember-kitchen", "#ea580c"),
    ("Lunar Fitness", "lunar-fitness", "#db2777"),
)
PLATFORMS = ("facebook", "instagram", "tiktok", "youtube")
LABELS = ("Facebook Page", "Instagram Business", "TikTok", "YouTube")
DESCRIPTION = "Local performance sample; no provider connection."


def seed(database: Path, project: str):
    database = database.expanduser().resolve()
    local_root = (Path.home() / ".apteva/apps/social/data").resolve()
    if not database.is_relative_to(local_root) or not database.is_file():
        raise ValueError("Use an existing database beneath ~/.apteva/apps/social/data (local dev only)")
    if not project.strip():
        raise ValueError("A local project ID is required")
    today = dt.datetime.now(dt.timezone.utc).date()
    now = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    result = {"database": str(database), "project_id": project, "profile_ids": [], "account_ids": []}
    with sqlite3.connect(database.as_uri() + "?mode=rw", uri=True, timeout=30) as db:
        # Hold a single transaction; any collision or unexpected state rolls
        # back all changes. Existing non-seed rows are never updated/deleted.
        db.execute("BEGIN IMMEDIATE")
        for brand_index, (name, slug, color) in enumerate(BRANDS):
            seed_slug = SOURCE + "-" + slug
            row = db.execute("SELECT id,description FROM profiles WHERE project_id=? AND slug=?", (project, seed_slug)).fetchone()
            if row and row[1] != DESCRIPTION:
                raise ValueError("Seed profile slug belongs to an existing profile")
            if row:
                profile = row[0]
            else:
                profile = db.execute("INSERT INTO profiles(project_id,name,slug,description,color) VALUES(?,?,?,?,?)", (project, name, seed_slug, DESCRIPTION, color)).lastrowid
            result["profile_ids"].append(profile)
            for platform_index, (platform, label) in enumerate(zip(PLATFORMS, LABELS)):
                identity = SOURCE + ":" + slug + ":" + platform
                row = db.execute("SELECT id,profile_id,platform,status,connection_id FROM social_accounts WHERE project_id=? AND provider_slug='native' AND provider_account_id=?", (project, identity)).fetchone()
                if row:
                    account, saved_profile, saved_platform, status, connection = row
                    if (saved_profile, saved_platform, status, connection) != (profile, platform, "disconnected", 0):
                        raise ValueError("Seed account has changed; refusing to overwrite it")
                else:
                    account = db.execute("""INSERT INTO social_accounts
                        (project_id,profile_id,platform,connection_id,external_account_id,display_name,status,provider_slug,provider_account_id)
                        VALUES(?,?,?,0,?,?,'disconnected','native',?)""", (project, profile, platform, identity, name + " · " + label, identity)).lastrowid
                result["account_ids"].append(account)
                ordinal = brand_index * 4 + platform_index + 1
                # Replace only observations owned by this seed. Unique source
                # keys and stable account IDs make reruns nonduplicating.
                db.execute("DELETE FROM social_metric_points WHERE project_id=? AND social_account_id=? AND source=?", (project, account, SOURCE))
                points = []
                for offset in range(180, 0, -1):
                    day = today - dt.timedelta(days=offset)
                    views = 180 + ordinal * 57 + (180 - offset) * (2 + ordinal // 4) + day.weekday() * 19
                    values = {"views": views, "impressions": views * 3 // 2 + 77, "likes": views // 16, "comments": views // 90, "shares": views // 65}
                    for metric, value in values.items():
                        points.append((project, profile, account, platform, metric, "day", day.isoformat() + "T00:00:00Z", value, SOURCE, DESCRIPTION))
                points.extend((project, profile, account, platform, metric, "snapshot", now, value, SOURCE, DESCRIPTION) for metric, value in {"followers": 2400 + ordinal * 1370, "_refresh": 1}.items())
                db.executemany("""INSERT INTO social_metric_points
                    (project_id,profile_id,social_account_id,platform,scope,metric,period,point_time,value,source,status,note)
                    VALUES(?,?,?,?,'account',?,?,?,?,?,'ok',?)""", points)
        result["metric_points"] = db.execute("SELECT COUNT(*) FROM social_metric_points WHERE project_id=? AND source=?", (project, SOURCE)).fetchone()[0]
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--project-id", required=True)
    args = parser.parse_args()
    print(json.dumps(seed(args.database, args.project_id)))
