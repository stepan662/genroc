package api

import (
	"encoding/json"
	"fmt"
	"time"

	"genroc/internal/db"
	"genroc/internal/model"
)

// decodeLogData unpacks the stored log-data envelope into the API view: a small inline
// payload comes back as its string value; an externalized one comes back as a bare
// reference with no inline data (the full value is fetched on demand via the log-object
// endpoint, or inlined by the caller with ?resolve=true). A non-envelope value is
// returned verbatim.
func decodeLogData(raw string) (string, *LogDataRef) {
	if raw == "" {
		return "", nil
	}
	var env model.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return raw, nil
	}
	if env.IsRef() {
		return "", &LogDataRef{Ref: env.Refs[0].Ref, Size: env.Refs[0].Size}
	}
	s, _ := env.Data.(string)
	return s, nil
}

func (h *Handlers) listInstanceLogs(id string, raw json.RawMessage) Reply {
	if id == "" {
		return errReply(fmt.Errorf("id is required"))
	}
	req := decodeOptionalBody[ListLogsReq](raw)
	opts := db.LogQuery{
		Level: req.Level,
		Since: req.Since,
		Page:  req.page(),
	}
	var (
		logs []*model.LogEntry
		info db.PageInfo
		err  error
	)
	if req.Recursive {
		logs, info, err = h.db.ListTreeLogs(id, opts)
	} else {
		logs, info, err = h.db.ListLogs(id, opts)
	}
	if err != nil {
		return errReply(err)
	}
	resp := make([]LogEntryResp, len(logs))
	for i, l := range logs {
		data, ref := decodeLogData(l.Data)
		// With resolve=true, replace the preview + data_ref with the full payload
		// inline. The object is owned by the log's own instance (l.InstanceID), which
		// differs from the queried root for subtree logs. Log objects are stored
		// pre-redacted, so serving them inline leaks nothing the data_ref didn't.
		if req.Resolve && ref != nil {
			if content, oerr := h.db.GetLogObject(l.InstanceID, ref.Ref); oerr == nil {
				data, ref = content, nil
			}
		}
		resp[i] = LogEntryResp{
			Time:     l.CreatedAt.Format(time.RFC3339Nano),
			Instance: l.InstanceID,
			Depth:    l.Depth,
			Level:    l.Level,
			Event:    l.Event,
			Task:     l.TaskID,
			Message:  l.Message,
			Code:     l.Code,
			Data:     data,
			DataRef:  ref,
			Meta:     l.Meta,
		}
	}
	return okReply(PageResp[LogEntryResp]{Items: resp, Page: info})
}

// getLogObject returns the full payload of an externalized log entry (referenced by a
// log row's data_ref). Only log objects are served — they are stored pre-redacted, so
// returning the raw content leaks no secrets.
func (h *Handlers) getLogObject(id, hash string) Reply {
	if id == "" || hash == "" {
		return errReply(fmt.Errorf("id and ref are required"))
	}
	content, err := h.db.GetLogObject(id, hash)
	if err != nil {
		return errReply(fmt.Errorf("log payload not found"))
	}
	return okReply(map[string]any{"data": content})
}
