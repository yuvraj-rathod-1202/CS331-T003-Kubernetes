import { useEffect, useRef } from 'react';
import { Terminal } from 'lucide-react';

export interface LogEntry {
  timestamp: string;
  message: string;
  type: 'info' | 'error' | 'success' | 'system' | 'warning' | 'critical';
}

interface LogTerminalProps {
  logs: LogEntry[];
}

export function LogTerminal({ logs }: LogTerminalProps) {
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [logs]);

  return (
    <div className="log-terminal glass-panel">
      <div className="panel-header">
        <Terminal className="icon" size={20} color="#64748b" />
        <h2>Live Event Stream</h2>
      </div>
      
      <div className="terminal-window" ref={scrollRef}>
        {logs.length === 0 ? (
          <div className="log-line system">Waiting for cluster events...</div>
        ) : (
          logs.map((log, i) => (
            <div key={i} className={`log-line ${log.type}`}>
              <span className="log-time">[{log.timestamp}]</span> {log.message}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
