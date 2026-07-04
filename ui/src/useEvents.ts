import { useEffect, useRef, useState } from "react";
import type { DeployEvent } from "./api";

// useEventStream subscribes to the SSE deploy-event feed and invokes onEvent for
// each event. It reconnects automatically if the stream drops.
export function useEventStream(onEvent: (e: DeployEvent) => void) {
  const cbRef = useRef(onEvent);
  cbRef.current = onEvent;

  useEffect(() => {
    let es: EventSource | null = null;
    let closed = false;
    let retry: number | undefined;

    const connect = () => {
      if (closed) return;
      es = new EventSource("/api/events/stream");
      es.addEventListener("deploy_event", (ev) => {
        try {
          cbRef.current(JSON.parse((ev as MessageEvent).data));
        } catch {
          /* ignore malformed */
        }
      });
      es.onerror = () => {
        es?.close();
        if (!closed) retry = window.setTimeout(connect, 3000);
      };
    };
    connect();

    return () => {
      closed = true;
      if (retry) clearTimeout(retry);
      es?.close();
    };
  }, []);
}

// useLiveEvents keeps a rolling list of the most recent events, seeded from an
// initial fetch and updated live via SSE.
export function useLiveEvents(initial: DeployEvent[], max = 100) {
  const [events, setEvents] = useState<DeployEvent[]>(initial);

  useEffect(() => setEvents(initial), [initial]);

  useEventStream((e) => {
    setEvents((prev) => {
      // Replace an existing row with the same id (status transitions), else prepend.
      const idx = prev.findIndex((p) => p.id === e.id);
      if (idx >= 0) {
        const next = prev.slice();
        next[idx] = e;
        return next;
      }
      return [e, ...prev].slice(0, max);
    });
  });

  return events;
}
