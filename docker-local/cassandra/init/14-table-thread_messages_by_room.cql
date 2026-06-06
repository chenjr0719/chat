CREATE TABLE IF NOT EXISTS chat.thread_messages_by_room (
  room_id                  TEXT,
  bucket                   BIGINT,
  thread_room_id           TEXT,
  created_at               TIMESTAMP,
  message_id               TEXT,
  thread_parent_id         TEXT,
  thread_parent_created_at TIMESTAMP,
  sender                   FROZEN<"Participant">,
  msg                      TEXT,
  mentions                 SET<FROZEN<"Participant">>,
  attachments              LIST<BLOB>,
  file                     FROZEN<"File">,
  card                     FROZEN<"Card">,
  card_action              FROZEN<"CardAction">,
  quoted_parent_message    FROZEN<"QuotedParentMessage">,
  visible_to               TEXT,
  reactions                MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>,
  deleted                  BOOLEAN,
  type                     TEXT,
  sys_msg_data             BLOB,
  site_id                  TEXT,
  edited_at                TIMESTAMP,
  updated_at               TIMESTAMP,
  PRIMARY KEY ((room_id, bucket), thread_room_id, created_at, message_id)
) WITH CLUSTERING ORDER BY (thread_room_id DESC, created_at DESC, message_id DESC)
  AND compaction = {
    'class': 'TimeWindowCompactionStrategy',
    'compaction_window_unit': 'HOURS',
    'compaction_window_size': '72'
  };

ALTER TABLE chat.thread_messages_by_room ADD enc_payload BLOB;
ALTER TABLE chat.thread_messages_by_room ADD enc_meta FROZEN<"EncMeta">;
