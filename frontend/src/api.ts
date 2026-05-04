export type NavItem = {
  title: string;
  link: string;
};

export type NavSection = {
  title: string;
  link: string;
  items: NavItem[];
};

export type HomeResponse = {
  categories: NavItem[];
  articles: NavItem[];
  landing: {
    title: string;
    content_html: string;
    link: string;
  };
};

export type WikiResponse = {
  mode: "article" | "directory";
  title: string;
  content_html?: string;
  content?: string;
  articles?: NavItem[];
  topics?: NavItem[];
  rel_path?: string;
};

export type SearchResult = {
  title: string;
  link: string;
  path: string;
  rendered_snippet?: string;
  plain_snippet?: string;
  highlighted_plain?: string;
};

export type SearchResponse = {
  query: string;
  total_count: number;
  indexed_at: string;
  results: SearchResult[];
};

export type Suggestion = {
  title: string;
  link: string;
  path: string;
  category: string;
};

async function fetchJson<T>(url: string): Promise<T> {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`Request failed: ${response.status}`);
  }
  return response.json() as Promise<T>;
}

export const api = {
  navigation: () => fetchJson<NavSection[]>("/api/navigation"),
  home: () => fetchJson<HomeResponse>("/api/home"),
  wiki: (relPath: string) => fetchJson<WikiResponse>(`/api/wiki?path=${encodeURIComponent(relPath)}`),
  search: (query: string) => fetchJson<SearchResponse>(`/api/search?q=${encodeURIComponent(query)}`),
  suggest: (query: string) => fetchJson<Suggestion[]>(`/api/search/suggest?q=${encodeURIComponent(query)}`)
};
