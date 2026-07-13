import { useEffect, useRef, useState } from "react";
import type { DeployEvent } from "./api";

// useEventStream subscribes to the SSE deploy-event feed and invokes onEvent for
// each event. It reconnects automatically if the stream drops, and returns
// whether the stream is currently connected.
export function useEventStream(onEvent: (e: DeployEvent) => void): boolean {
  const cbRef = useRef(onEvent);
  cbRef.current = onEvent;
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    let es: EventSource | null = null;
    let closed = false;
    let retry: number | undefined;

    const connect = () => {
      if (closed) return;
      es = new EventSource("/api/events/stream");
      es.onopen = () => setConnected(true);
      es.addEventListener("deploy_event", (ev) => {
        try {
          cbRef.current(JSON.parse((ev as MessageEvent).data));
        } catch {
          /* ignore malformed */
        }
      });
      es.onerror = () => {
        setConnected(false);
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

  return connected;
}

// useLiveEvents keeps a rolling list of the most recent events, seeded from an
// initial fetch and updated live via SSE.
export function useLiveEvents(initial: DeployEvent[], max = 100) {
  const [events, setEvents] = useState<DeployEvent[]>(initial);

  // Merge the fetched list under any events that streamed in while the fetch
  // was in flight (the SSE stream connects before the fetch resolves). A
  // streamed row with the same id is newer than its fetched copy, so it wins.
  useEffect(() => {
    setEvents((prev) => {
      if (prev.length === 0) return initial;
      const seen = new Set(prev.map((p) => p.id));
      return [...prev, ...initial.filter((e) => !seen.has(e.id))]
        .sort((a, b) => b.id - a.id)
        .slice(0, max);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initial]);

  const connected = useEventStream((e) => {
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

  return { events, connected };
}
