import { useEffect, useRef, useState } from 'react';
import { apiGet, fileContentPath, filesPath } from '../api/client';
import type { FileEntry, FileListResult, FileReadResult } from '../api/types';

export function FilePreview({ preview }: { preview: FileReadResult | null }) {
  if (!preview) return <div className="file-preview-empty">No file selected</div>;
  if (preview.too_large) {
    return (
      <div className="file-preview">
        <strong>{preview.path}</strong>
        <p>文件超过 2MB, 不预览。</p>
      </div>
    );
  }
  if (preview.binary) {
    return (
      <div className="file-preview">
        <strong>{preview.path}</strong>
        <p>二进制文件 · {preview.size} bytes</p>
      </div>
    );
  }
  return (
    <pre className="file-preview">
      <code>{preview.content || ''}</code>
    </pre>
  );
}

export function FileExplorerPanel({ daemonID, sessionID }: { daemonID: string; sessionID: string }) {
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [preview, setPreview] = useState<FileReadResult | null>(null);
  const [error, setError] = useState('');
  const previewRequestRef = useRef(0);

  useEffect(() => {
    let cancelled = false;
    previewRequestRef.current += 1;
    setEntries([]);
    setPreview(null);
    setError('');

    if (!daemonID || !sessionID) return;

    apiGet<FileListResult>(filesPath(daemonID, sessionID, '.'))
      .then((result) => {
        if (!cancelled) setEntries(result.entries || []);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      });

    return () => {
      cancelled = true;
    };
  }, [daemonID, sessionID]);

  async function openFile(entry: FileEntry) {
    if (entry.kind !== 'file' || !daemonID || !sessionID) return;
    const requestID = previewRequestRef.current + 1;
    previewRequestRef.current = requestID;
    setPreview(null);
    setError('');
    try {
      const result = await apiGet<FileReadResult>(fileContentPath(daemonID, sessionID, entry.path));
      if (previewRequestRef.current === requestID) setPreview(result);
    } catch (err) {
      if (previewRequestRef.current === requestID) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  }

  return (
    <aside className="file-panel">
      <div className="file-list">
        {entries.map((entry) => (
          <button
            key={entry.path}
            className="file-row"
            disabled={entry.kind !== 'file'}
            onClick={() => void openFile(entry)}
            type="button"
          >
            <span className="file-kind">{entry.kind === 'file' ? 'FILE' : 'DIR'}</span>
            <span className="file-name">{entry.name}</span>
          </button>
        ))}
      </div>
      {error ? <div className="file-error">{error}</div> : null}
      <FilePreview preview={preview} />
    </aside>
  );
}
