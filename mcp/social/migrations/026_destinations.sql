ALTER TABLE social_accounts ADD COLUMN default_destination_id TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN default_destination_name TEXT NOT NULL DEFAULT '';
ALTER TABLE social_accounts ADD COLUMN account_kind TEXT NOT NULL DEFAULT 'account';

-- Preserve legacy native board presets and every target's intended board.
UPDATE social_accounts SET account_kind='destination_preset',
 default_destination_id=COALESCE(external_account_id,''),default_destination_name=display_name
WHERE platform='pinterest' AND COALESCE(provider_slug,'native')='native'
 AND COALESCE(external_account_id,'')<>'';
UPDATE post_targets SET options=json_set(CASE WHEN json_valid(options) THEN options ELSE '{}' END,
 '$.destination_id',(SELECT default_destination_id FROM social_accounts WHERE id=post_targets.social_account_id))
WHERE social_account_id IN (SELECT id FROM social_accounts WHERE account_kind='destination_preset')
 AND COALESCE(NULLIF(json_extract(CASE WHEN json_valid(options) THEN options ELSE '{}' END,'$.destination_id'),''),
 NULLIF(json_extract(CASE WHEN json_valid(options) THEN options ELSE '{}' END,'$.board_id'),''),
 NULLIF(json_extract(CASE WHEN json_valid(options) THEN options ELSE '{}' END,'$.boardId'),''),'')='';

-- Preserve historical board series under the actual board, never a mixed account total.
UPDATE social_metric_points SET scope='destination:'||(SELECT default_destination_id FROM social_accounts WHERE id=social_metric_points.social_account_id)
WHERE scope='account' AND social_account_id IN (SELECT id FROM social_accounts WHERE account_kind='destination_preset');

-- Zernio connected-account snapshots can contain a saved default. Keep it local
-- and freeze it into old targets before any future default change.
UPDATE social_accounts SET default_destination_id=COALESCE(
 json_extract(CASE WHEN json_valid(provider_data) THEN provider_data ELSE '{}' END,'$.defaultBoardId'),
 json_extract(CASE WHEN json_valid(provider_data) THEN provider_data ELSE '{}' END,'$.platformSpecificData.boardId'),
 NULLIF(json_extract(CASE WHEN json_valid(provider_data) THEN provider_data ELSE '{}' END,'$.boardId'),''),'')
WHERE platform='pinterest' AND provider_slug='zernio';
UPDATE post_targets SET options=json_set(CASE WHEN json_valid(options) THEN options ELSE '{}' END,
 '$.destination_id',(SELECT default_destination_id FROM social_accounts WHERE id=post_targets.social_account_id))
WHERE social_account_id IN (SELECT id FROM social_accounts WHERE platform='pinterest' AND provider_slug='zernio' AND default_destination_id<>'')
 AND COALESCE(NULLIF(json_extract(CASE WHEN json_valid(options) THEN options ELSE '{}' END,'$.destination_id'),''),
 NULLIF(json_extract(CASE WHEN json_valid(options) THEN options ELSE '{}' END,'$.board_id'),''),
 NULLIF(json_extract(CASE WHEN json_valid(options) THEN options ELSE '{}' END,'$.boardId'),''),'')='';
