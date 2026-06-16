export interface FilePreviewData {
  path: string;
  size: number;
  mime?: string;
  binary?: boolean;
  too_large?: boolean;
  content?: string;
}

export function FilePreview({ preview }: { preview: FilePreviewData | null }) {
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

export function FileExplorerPanel() {
  return (
    <aside className="file-panel">
      <FilePreview preview={null} />
    </aside>
  );
}
