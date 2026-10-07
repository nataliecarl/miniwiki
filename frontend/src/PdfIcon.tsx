// A document with a folded corner and a "PDF" label, marking links that open
// a PDF in the viewer. Sized in em so it follows the surrounding text.
export function PdfIcon({ className }: { className?: string }) {
  return (
    <svg
      className={`mw-pdf-icon ${className || ""}`}
      viewBox="0 0 24 24"
      width="1em"
      height="1em"
      aria-label="PDF"
      role="img">
      <path
        d="M6 2h8l6 6v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinejoin="round"
      />
      <path d="M14 2v6h6" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
      <text x="12" y="18" textAnchor="middle" fontSize="6.5" fontWeight="700" fill="currentColor" fontFamily="Arial, sans-serif">
        PDF
      </text>
    </svg>
  );
}
