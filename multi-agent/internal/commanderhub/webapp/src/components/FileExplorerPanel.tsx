import { useEffect, useRef, useState } from 'react';
import type { CSSProperties, ReactNode } from 'react';
import { ChevronDown, ChevronRight, Copy } from 'lucide-react';
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

type DirectoryNode = {
  expanded: boolean;
  entries?: FileEntry[];
  loading?: boolean;
};

export function isAbsolutePath(path: string) {
  return path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith('\\\\');
}

export function fullPath(root: string, path: string) {
  if (!root || path === '.' || isAbsolutePath(path)) return path;
  const separator = root.includes('\\') ? '\\' : '/';
  const cleanRoot = root.replace(/[\\/]+$/, '');
  const cleanPath = path.replace(/^[\\/]+/, '').replace(/[\\/]+/g, separator);
  return `${cleanRoot}${separator}${cleanPath}`;
}

export function FileExplorerPanel({
  daemonID,
  sessionID,
  renderMode = 'inline',
  onPreview,
}: {
  daemonID: string;
  sessionID: string;
  renderMode?: 'inline' | 'sheet';
  onPreview?: (payload: {
    preview: FileReadResult;
    fullPath: string;
    displayPath: string;
  }) => void;
}) {
  const [root, setRoot] = useState('');
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [directories, setDirectories] = useState<Record<string, DirectoryNode>>({});
  const [preview, setPreview] = useState<FileReadResult | null>(null);
  const [error, setError] = useState('');
  const previewRequestRef = useRef(0);
  const listingRequestRef = useRef(0);

  useEffect(() => {
    let cancelled = false;
    previewRequestRef.current += 1;
    listingRequestRef.current += 1;
    const requestID = listingRequestRef.current;
    setRoot('');
    setEntries([]);
    setDirectories({});
    setPreview(null);
    setError('');

    if (!daemonID || !sessionID) return;

    apiGet<FileListResult>(filesPath(daemonID, sessionID, '.'))
      .then((result) => {
        if (!cancelled && listingRequestRef.current === requestID) {
          setRoot(result.root || '');
          setEntries(result.entries || []);
        }
      })
      .catch((err: Error) => {
        if (!cancelled && listingRequestRef.current === requestID) setError(err.message);
      });

    return () => {
      cancelled = true;
    };
  }, [daemonID, sessionID]);

  async function openFile(entry: FileEntry) {
    if (entry.kind !== 'file' || !daemonID || !sessionID) return;
    const requestID = previewRequestRef.current + 1;
    previewRequestRef.current = requestID;
    if (renderMode === 'inline') setPreview(null);
    setError('');
    try {
      const result = await apiGet<FileReadResult>(fileContentPath(daemonID, sessionID, entry.path));
      if (previewRequestRef.current !== requestID) return;
      if (renderMode === 'sheet') {
        onPreview?.({
          preview: result,
          fullPath: fullPath(root, entry.path),
          displayPath: entry.path,
        });
        return;
      }
      setPreview(result);
    } catch (err) {
      if (previewRequestRef.current === requestID) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  }

  async function toggleDirectory(entry: FileEntry) {
    if (entry.kind !== 'dir' || !daemonID || !sessionID) return;

    const current = directories[entry.path];
    if (current?.expanded) {
      setDirectories((prev) => ({
        ...prev,
        [entry.path]: { ...prev[entry.path], expanded: false },
      }));
      return;
    }

    setDirectories((prev) => ({
      ...prev,
      [entry.path]: { ...(prev[entry.path] || {}), expanded: true, loading: !prev[entry.path]?.entries },
    }));
    if (current?.entries) return;

    const requestID = listingRequestRef.current;
    try {
      const result = await apiGet<FileListResult>(filesPath(daemonID, sessionID, entry.path));
      if (listingRequestRef.current !== requestID) return;
      setRoot((current) => current || result.root || '');
      setDirectories((prev) => ({
        ...prev,
        [entry.path]: { expanded: true, entries: result.entries || [], loading: false },
      }));
    } catch (err) {
      if (listingRequestRef.current !== requestID) return;
      setDirectories((prev) => ({
        ...prev,
        [entry.path]: { ...(prev[entry.path] || {}), expanded: false, loading: false },
      }));
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function copyPath(path: string) {
    try {
      await navigator.clipboard.writeText(path);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function renderEntries(items: FileEntry[], depth = 0): ReactNode {
    return items.map((entry) => {
      const isDir = entry.kind === 'dir';
      const dir = directories[entry.path];
      const isExpanded = !!dir?.expanded;
      return (
        <div className="file-node" key={entry.path}>
          <div className="file-row-line" style={{ '--depth': depth } as CSSProperties}>
            <button
              aria-label={isDir ? `${isExpanded ? '收起' : '展开'}目录 ${entry.name}` : `打开文件 ${entry.name}`}
              className="file-row"
              onClick={() => (isDir ? void toggleDirectory(entry) : void openFile(entry))}
              type="button"
            >
              <span className="file-kind">
                {isDir ? (isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />) : 'FILE'}
              </span>
              <span className="file-name">{entry.name}</span>
            </button>
            <button
              aria-label={`复制路径 ${entry.path}`}
              className="file-copy-button"
              onClick={() => void copyPath(fullPath(root, entry.path))}
              title="复制路径"
              type="button"
            >
              <Copy size={14} />
            </button>
          </div>
          {isDir && isExpanded ? (
            <div className="file-children">
              {dir?.loading ? <div className="file-loading">Loading</div> : renderEntries(dir?.entries || [], depth + 1)}
            </div>
          ) : null}
        </div>
      );
    });
  }

  return (
    <aside className="file-panel" data-testid="file-panel">
      <div className="file-list">{renderEntries(entries)}</div>
      {error ? <div className="file-error">{error}</div> : null}
      {renderMode === 'inline' ? <FilePreview preview={preview} /> : null}
    </aside>
  );
}
