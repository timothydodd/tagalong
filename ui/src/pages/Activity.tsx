import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, type DeployEvent } from "../api";
import { StatusBadge, timeAgo, tagOf } from "../components";
import { useLiveEvents } from "../useEvents";

export default function Activity() {
  const [initial, setInitial] = useState<DeployEvent[]>([]);
  const events = useLiveEvents(initial);

  useEffect(() => {
    api.listEvents({ limit: 100 }).then(setInitial).catch(() => {});
  }, []);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Activity</h1>
          <div className="sub flex">
            <span className="badge success" style={{ background: "transparent" }}>
              live
            </span>
            Deploy events across all apps, streamed in real time.
          </div>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        {events.length === 0 ? (
          <div className="empty">No activity yet.</div>
        ) : (
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
                      <span className="mono" style={{ fontSize: 12 }}>
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
        )}
      </div>
    </>
  );
}
