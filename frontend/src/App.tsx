import { FormEvent, KeyboardEvent, useEffect, useMemo, useState } from "react";
import { Link, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { api, HomeResponse, NavSection, SearchResponse, Suggestion, WikiResponse } from "./api";

function useSearchQuery(): string {
  const location = useLocation();
  return useMemo(() => new URLSearchParams(location.search).get("q")?.trim() || "", [location.search]);
}

function SearchBox({
  initialValue,
  placeholder,
  autoFocusSelect,
  className,
  inputClassName
}: {
  initialValue?: string;
  placeholder: string;
  autoFocusSelect?: boolean;
  className?: string;
  inputClassName?: string;
}) {
  const navigate = useNavigate();
  const [value, setValue] = useState(initialValue || "");
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);

  useEffect(() => {
    setValue(initialValue || "");
  }, [initialValue]);

  useEffect(() => {
    const query = value.trim();
    if (query.length < 2) {
      setSuggestions([]);
      setOpen(false);
      return;
    }
    const timeoutId = window.setTimeout(async () => {
      try {
        const items = await api.suggest(query);
        setSuggestions(items);
        setOpen(items.length > 0);
        setActiveIndex(-1);
      } catch {
        setSuggestions([]);
        setOpen(false);
      }
    }, 120);
    return () => window.clearTimeout(timeoutId);
  }, [value]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const query = value.trim();
    navigate(query ? `/search?q=${encodeURIComponent(query)}` : "/search");
    setOpen(false);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (!open || !suggestions.length) {
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActiveIndex((prev) => (prev + 1) % suggestions.length);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActiveIndex((prev) => (prev <= 0 ? suggestions.length - 1 : prev - 1));
    } else if (event.key === "Enter" && activeIndex >= 0) {
      event.preventDefault();
      navigate(suggestions[activeIndex].link);
      setOpen(false);
    } else if (event.key === "Escape") {
      setOpen(false);
    }
  };

  return (
    <form className={`mw-search ${className || ""}`} onSubmit={submit}>
      <input
        className={`mw-search-input ${inputClassName || ""}`}
        type="search"
        value={value}
        placeholder={placeholder}
        onChange={(event) => setValue(event.target.value)}
        onKeyDown={onKeyDown}
        onBlur={() => window.setTimeout(() => setOpen(false), 110)}
        onFocus={(event) => {
          if (autoFocusSelect) {
            event.currentTarget.select();
          }
          if (suggestions.length > 0) {
            setOpen(true);
          }
        }}
        autoFocus={autoFocusSelect}
      />
      {open && suggestions.length > 0 && (
        <div className="mw-suggest-box">
          {suggestions.map((suggestion, idx) => (
            <a
              key={`${suggestion.link}-${idx}`}
              href={suggestion.link}
              className={`mw-suggest-item ${idx === activeIndex ? "active" : ""}`}
              onMouseDown={(event) => {
                event.preventDefault();
                navigate(suggestion.link);
                setOpen(false);
              }}>
              <span className="mw-suggest-title">{suggestion.title}</span>
              <span className="mw-suggest-meta">{suggestion.category}</span>
            </a>
          ))}
        </div>
      )}
    </form>
  );
}

function HomePage() {
  const [data, setData] = useState<HomeResponse | null>(null);

  useEffect(() => {
    void api.home().then(setData).catch(() => setData({ categories: [], articles: [] }));
  }, []);

  return (
    <div className="mw-page-stack">
      <div className="mw-page-heading">
        <h1>MiniWiki</h1>
      </div>
      <article className="mw-article mw-landing-article" dangerouslySetInnerHTML={{ __html: data?.landing.content_html || "" }} />
    </div>
  );
}

function WikiPage() {
  const location = useLocation();
  const relPath = decodeURIComponent(location.pathname.replace(/^\/wiki\/?/, ""));
  const [data, setData] = useState<WikiResponse | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!relPath) {
      setError("No page selected.");
      return;
    }
    setError("");
    setData(null);
    void api
      .wiki(relPath)
      .then(setData)
      .catch(() => setError("Page not found."));
  }, [relPath]);

  if (error) {
    return <p className="mw-muted">{error}</p>;
  }
  if (!data) {
    return <p className="mw-muted">Loading...</p>;
  }
  if (data.mode === "directory") {
    return (
      <div className="mw-page-stack mw-directory-page">
        <div className="mw-page-heading">
          <h1>{data.title}</h1>
        </div>
        {!!data.articles?.length && (
          <section className="mw-card mw-directory-card">
            <h2 className="mw-directory-heading">Articles</h2>
            <ul className="mw-directory-list">
              {data.articles.map((item) => (
                <li key={item.link}>
                  <Link className="mw-directory-link" to={item.link}>
                    {item.title}
                  </Link>
                </li>
              ))}
            </ul>
          </section>
        )}
        {!!data.topics?.length && (
          <section className="mw-card mw-directory-card">
            <h2 className="mw-directory-heading">Topics</h2>
            <ul className="mw-directory-list">
              {data.topics.map((item) => (
                <li key={item.link}>
                  <Link className="mw-directory-link" to={item.link}>
                    {item.title}
                  </Link>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    );
  }
  return (
    <article className="mw-article" dangerouslySetInnerHTML={{ __html: data.content_html || data.content || "" }} />
  );
}

function SearchPage() {
  const navigate = useNavigate();
  const query = useSearchQuery();
  const [data, setData] = useState<SearchResponse | null>(null);
  const resultCount = data?.total_count || 0;
  const resultLabel = resultCount === 1 ? "result" : "results";

  useEffect(() => {
    void api.search(query).then(setData).catch(() => setData({ query, total_count: 0, indexed_at: "", results: [] }));
  }, [query]);

  return (
    <div className="mw-page-stack">
      <div className="mw-page-heading">
        <h1>Search</h1>
        <p>{resultCount} {resultLabel} for search "{query}"</p>
      </div>
      <div className="mw-search-results">
        {data?.results.map((result) => (
          <section
            className="mw-result mw-result-clickable"
            key={result.link}
            role="link"
            tabIndex={0}
            onClick={() => navigate(result.link)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                navigate(result.link);
              }
            }}>
            <div className="mw-result-title">
              {result.title}
            </div>
            <div className="mw-result-path">{result.path}</div>
            {result.rendered_snippet ? (
              <div className="mw-snippet" dangerouslySetInnerHTML={{ __html: result.rendered_snippet }} />
            ) : result.highlighted_plain ? (
              <p className="mw-muted" dangerouslySetInnerHTML={{ __html: result.highlighted_plain }} />
            ) : null}
          </section>
        ))}
      </div>
    </div>
  );
}

export default function App() {
  const location = useLocation();
  const query = useSearchQuery();
  const [sections, setSections] = useState<NavSection[]>([]);
  const focusHeaderSearch = location.pathname === "/";

  useEffect(() => {
    void api.navigation().then(setSections).catch(() => setSections([]));
  }, []);

  return (
    <div className="mw-shell">
      <header className="mw-header">
        <div className="mw-header-inner">
          <Link to="/" className="mw-brand">
            MiniWiki
          </Link>
          <SearchBox initialValue={query} placeholder="Search pages..." autoFocusSelect={focusHeaderSearch} />
        </div>
      </header>
      <div className="mw-body">
        <aside className="mw-sidebar">
          {sections.map((section) => (
            <div key={section.link} className="mw-sidebar-section">
              <Link className="mw-sidebar-section-link" to={section.link}>
                {section.title}
              </Link>
              <ul className="mw-sidebar-list">
                {section.items.map((item) => (
                  <li key={item.link}>
                    <Link to={item.link}>{item.title}</Link>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </aside>
        <main className="mw-main">
          <div className="mw-main-inner">
            <Routes>
              <Route path="/" element={<HomePage />} />
              <Route path="/wiki/*" element={<WikiPage />} />
              <Route path="/search" element={<SearchPage />} />
            </Routes>
          </div>
        </main>
      </div>
    </div>
  );
}
