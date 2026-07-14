import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type DeployEvent } from "../api";
import { StatusBadge, timeAgo, tagOf } from "../components";
import { useLiveEvents } from "../useEvents";

const PAGE = 50;

export default function Activity() {
  const [initial, setInitial] = useState<DeployEvent[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  // Keep enough room for many pages plus live updates.
  const { events, connected } = useLiveEvents(initial, 5000);

  useEffect(() => {
    api
      .listEvents({ limit: PAGE })
      .then((list) => {
        setInitial(list);
        setHasMore(list.length === PAGE);
      })
      .catch(() => {});
  }, []);

  const loadMore = async () => {
    const oldest = events[events.length - 1];
    if (!oldest) return;
    setLoadingMore(true);
    try {
      const older = await api.listEvents({ before_id: oldest.id, limit: PAGE });
      // Append to initial so useLiveEvents merges & re-sorts them in.
      setInitial((prev) => [...prev, ...older]);
      setHasMore(older.length === PAGE);
    } catch {
      /* ignore */
    } finally {
      setLoadingMore(false);
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Activity</h1>
          <div className="sub flex">
            <span
              className={`badge ${connected ? "success" : "unknown"}`}
              style={{ background: "transparent" }}
            >
              {connected ? "live" : "reconnecting…"}
            </span>
            Deploy events across all apps, streamed in real time.
          </div>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        {events.length === 0 ? (
          <div className="empty">No activity yet.</div>
        ) : (
          <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>App</th>
                <th>Trigger</th>
                <th>Change</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id}>
                  <td title={e.started_at}>{timeAgo(e.started_at)}</td>
                  <td>
                    {e.app_id ? (
                      <Link to={`/apps/${e.app_id}`} style={{ fontWeight: 600 }}>
                        {e.app_name}
                      </Link>
                    ) : (
                      e.app_name
                    )}
                  </td>
                  <td>
                    <span className="tag">{e.trigger}</span>
                  </td>
                  <td>
                    {e.action === "restart" ? (
                      <span className="muted">restart</span>
                    ) : (
                      <span className="mono trunc" style={{ fontSize: 12 }} title={e.new_image}>
                        {tagOf(e.new_image)}
                      </span>
                    )}
                    {e.detail && (
                      <div className="faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                        {e.detail}
                      </div>
                    )}
                  </td>
                  <td>
                    <StatusBadge status={e.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
        {hasMore && (
          <div className="flex" style={{ justifyContent: "center", padding: 12 }}>
            <button className="btn" onClick={loadMore} disabled={loadingMore}>
              {loadingMore ? "Loading…" : "Load more"}
            </button>
          </div>
        )}
      </div>
    </>
  );
}
