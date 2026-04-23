(() => {
    const inputs = document.querySelectorAll('input[data-search-suggest="true"]');
    if (!inputs.length) {
        return;
    }

    const debounce = (fn, wait) => {
        let timeoutId = null;
        return (...args) => {
            window.clearTimeout(timeoutId);
            timeoutId = window.setTimeout(() => fn(...args), wait);
        };
    };

    for (const input of inputs) {
        const form = input.closest("form");
        if (!form) {
            continue;
        }
        form.classList.add("search-suggest-host");

        const box = document.createElement("div");
        box.className = "search-suggest-box";
        box.hidden = true;
        form.appendChild(box);

        let suggestions = [];
        let activeIndex = -1;
        let abortController = null;
        let isPointerInsideBox = false;

        const hideBox = () => {
            box.hidden = true;
            box.innerHTML = "";
            suggestions = [];
            activeIndex = -1;
        };

        const applyActiveState = () => {
            const nodes = box.querySelectorAll(".search-suggest-item");
            nodes.forEach((node, idx) => {
                if (idx === activeIndex) {
                    node.classList.add("active");
                } else {
                    node.classList.remove("active");
                }
            });
        };

        const renderSuggestions = (items) => {
            suggestions = items;
            activeIndex = -1;
            if (!items.length) {
                hideBox();
                return;
            }
            box.innerHTML = "";
            for (const item of items) {
                const a = document.createElement("a");
                a.className = "search-suggest-item";
                a.href = item.link;
                a.innerHTML = `
                    <span class="search-suggest-title"></span>
                    <span class="search-suggest-path"></span>
                    <span class="search-suggest-preview"></span>
                `;
                a.querySelector(".search-suggest-title").textContent = item.title;
                a.querySelector(".search-suggest-path").textContent = item.path;
                const preview = a.querySelector(".search-suggest-preview");
                if (item.match_preview) {
                    preview.textContent = item.match_preview;
                } else {
                    preview.remove();
                }
                box.appendChild(a);
            }
            box.hidden = false;
        };

        box.addEventListener("pointerdown", (event) => {
            const item = event.target.closest(".search-suggest-item");
            if (!item) {
                return;
            }
            event.preventDefault();
            window.location.assign(item.href);
        });

        box.addEventListener("mouseenter", () => {
            isPointerInsideBox = true;
        });

        box.addEventListener("mouseleave", () => {
            isPointerInsideBox = false;
        });

        const fetchSuggestions = async (rawQuery) => {
            const query = rawQuery.trim();
            if (query.length < 2) {
                hideBox();
                return;
            }
            if (abortController) {
                abortController.abort();
            }
            abortController = new AbortController();
            try {
                const response = await fetch(`/search/suggest?q=${encodeURIComponent(query)}`, {
                    signal: abortController.signal,
                });
                if (!response.ok) {
                    hideBox();
                    return;
                }
                const items = await response.json();
                if (input.value.trim() !== query) {
                    return;
                }
                renderSuggestions(Array.isArray(items) ? items : []);
            } catch (_err) {
                hideBox();
            }
        };

        const debouncedFetch = debounce(fetchSuggestions, 120);

        input.addEventListener("input", (event) => {
            debouncedFetch(event.target.value);
        });

        input.addEventListener("keydown", (event) => {
            if (box.hidden || !suggestions.length) {
                return;
            }
            if (event.key === "ArrowDown") {
                event.preventDefault();
                activeIndex = (activeIndex + 1) % suggestions.length;
                applyActiveState();
            } else if (event.key === "ArrowUp") {
                event.preventDefault();
                activeIndex = activeIndex <= 0 ? suggestions.length - 1 : activeIndex - 1;
                applyActiveState();
            } else if (event.key === "Enter" && activeIndex >= 0) {
                event.preventDefault();
                window.location.assign(suggestions[activeIndex].link);
            } else if (event.key === "Escape") {
                hideBox();
            }
        });

        input.addEventListener("blur", () => {
            window.setTimeout(() => {
                if (!isPointerInsideBox) {
                    hideBox();
                }
            }, 120);
        });

        input.addEventListener("focus", () => {
            if (suggestions.length) {
                box.hidden = false;
            }
        });

        document.addEventListener("pointerdown", (event) => {
            if (!form.contains(event.target)) {
                hideBox();
            }
        });
    }
})();
