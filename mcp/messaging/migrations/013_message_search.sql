CREATE TABLE message_recipients (
 message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 address TEXT NOT NULL, PRIMARY KEY(message_id,address)
);
INSERT OR IGNORE INTO message_recipients SELECT m.id,lower(j.value) FROM messages m,json_each(m.to_addrs) j;
INSERT OR IGNORE INTO message_recipients SELECT m.id,lower(j.value) FROM messages m,json_each(m.cc_addrs) j;
INSERT OR IGNORE INTO message_recipients SELECT m.id,lower(j.value) FROM messages m,json_each(m.bcc_addrs) j;
CREATE INDEX ix_message_recipients_address ON message_recipients(address,message_id);
CREATE TRIGGER messages_recipients_insert AFTER INSERT ON messages BEGIN
 INSERT OR IGNORE INTO message_recipients SELECT NEW.id,lower(value) FROM json_each(NEW.to_addrs);
 INSERT OR IGNORE INTO message_recipients SELECT NEW.id,lower(value) FROM json_each(NEW.cc_addrs);
 INSERT OR IGNORE INTO message_recipients SELECT NEW.id,lower(value) FROM json_each(NEW.bcc_addrs);
END;
CREATE TRIGGER messages_recipients_update AFTER UPDATE OF to_addrs,cc_addrs,bcc_addrs ON messages BEGIN
 DELETE FROM message_recipients WHERE message_id=NEW.id;
 INSERT OR IGNORE INTO message_recipients SELECT NEW.id,lower(value) FROM json_each(NEW.to_addrs);
 INSERT OR IGNORE INTO message_recipients SELECT NEW.id,lower(value) FROM json_each(NEW.cc_addrs);
 INSERT OR IGNORE INTO message_recipients SELECT NEW.id,lower(value) FROM json_each(NEW.bcc_addrs);
END;
CREATE VIRTUAL TABLE message_search USING fts5(sender,recipients,subject,body_text,body_html,tokenize='trigram');
INSERT INTO message_search(rowid,sender,recipients,subject,body_text,body_html)
 SELECT id,from_addr,to_addrs||' '||cc_addrs||' '||bcc_addrs,subject,body_text,body_html FROM messages;
CREATE TRIGGER messages_search_insert AFTER INSERT ON messages BEGIN
 INSERT INTO message_search(rowid,sender,recipients,subject,body_text,body_html) VALUES(NEW.id,NEW.from_addr,NEW.to_addrs||' '||NEW.cc_addrs||' '||NEW.bcc_addrs,NEW.subject,NEW.body_text,NEW.body_html);
END;
CREATE TRIGGER messages_search_delete AFTER DELETE ON messages BEGIN
 DELETE FROM message_search WHERE rowid=OLD.id;
END;
CREATE TRIGGER messages_search_update AFTER UPDATE OF from_addr,to_addrs,cc_addrs,bcc_addrs,subject,body_text,body_html ON messages BEGIN
 DELETE FROM message_search WHERE rowid=OLD.id;
 INSERT INTO message_search(rowid,sender,recipients,subject,body_text,body_html) VALUES(NEW.id,NEW.from_addr,NEW.to_addrs||' '||NEW.cc_addrs||' '||NEW.bcc_addrs,NEW.subject,NEW.body_text,NEW.body_html);
END;
