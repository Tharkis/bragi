// Bragi for the Jellyfin web client: a Request button in the header that
// opens a panel to find and request shows, a "My requests" list, and Bragi
// results under Jellyfin's own search results. Every call goes through the
// plugin's /Bragi endpoints, which add the Bragi key on the server.
(function () {
    'use strict';
    if (window.__bragiLoaded) return;
    window.__bragiLoaded = true;

    var state = { enabled: false, panel: null, tab: 'discover', searchTimer: null, lastSearch: '' };

    // ---- helpers ----

    function el(tag, attrs, children) {
        var e = document.createElement(tag);
        Object.keys(attrs || {}).forEach(function (k) {
            if (k === 'class') e.className = attrs[k];
            else if (k === 'text') e.textContent = attrs[k];
            else if (k.indexOf('on') === 0) e.addEventListener(k.slice(2), attrs[k]);
            else e.setAttribute(k, attrs[k]);
        });
        (children || []).forEach(function (c) { if (c) e.appendChild(c); });
        return e;
    }

    // Inline icons: Jellyfin only ships the Material icons it uses itself.
    var ICONS = {
        request: 'M14 10H3v2h11v-2zm0-4H3v2h11V6zm4 8v-4h-2v4h-4v2h4v4h2v-4h4v-2h-4zM3 16h7v-2H3v2z',
        close: 'M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z'
    };

    function icon(name) {
        var ns = 'http://www.w3.org/2000/svg';
        var svg = document.createElementNS(ns, 'svg');
        svg.setAttribute('viewBox', '0 0 24 24');
        svg.setAttribute('class', 'bragi-icon');
        svg.setAttribute('aria-hidden', 'true');
        var path = document.createElementNS(ns, 'path');
        path.setAttribute('d', ICONS[name]);
        path.setAttribute('fill', 'currentColor');
        svg.appendChild(path);
        return svg;
    }

    function api(method, path, body) {
        var headers = { 'Accept': 'application/json', 'Authorization': 'MediaBrowser Token="' + ApiClient.accessToken() + '"' };
        var opts = { method: method, headers: headers };
        if (body !== undefined) {
            headers['Content-Type'] = 'application/json';
            opts.body = JSON.stringify(body);
        }
        return fetch(ApiClient.getUrl('Bragi/' + path), opts).then(function (res) {
            if (res.status === 204) return null;
            return res.json().catch(function () { return null; }).then(function (data) {
                if (!res.ok) {
                    var err = new Error((data && data.error) || ('Bragi answered HTTP ' + res.status));
                    err.status = res.status;
                    err.data = data;
                    throw err;
                }
                return data;
            });
        });
    }

    function toast(text) {
        var t = el('div', { class: 'bragi-toast', role: 'status', text: text });
        document.body.appendChild(t);
        setTimeout(function () { t.classList.add('bragi-toast-show'); }, 10);
        setTimeout(function () { t.classList.remove('bragi-toast-show'); setTimeout(function () { t.remove(); }, 300); }, 3500);
    }

    // statusOf describes a discover result for its badge.
    function statusOf(r) {
        if (r.seriesId) return { label: 'In library', cls: 'bragi-ok' };
        if (r.requested === 'available') return { label: 'Available', cls: 'bragi-ok' };
        if (r.requested === 'approved') return { label: 'Requested', cls: 'bragi-busy' };
        if (r.requested === 'pending') return { label: 'Waiting for approval', cls: 'bragi-wait' };
        return null;
    }

    function requestLabel(rq) {
        switch (rq.status) {
            case 'pending': return { label: 'Waiting for approval', cls: 'bragi-wait' };
            case 'denied': return { label: 'Not approved', cls: 'bragi-bad' };
            case 'available': return { label: 'Available', cls: 'bragi-ok' };
            default:
                if (rq.progress && rq.progress.downloading > 0) return { label: 'Downloading', cls: 'bragi-busy' };
                return { label: 'Requested', cls: 'bragi-busy' };
        }
    }

    // ---- cards ----

    function card(r, onRequested) {
        var st = statusOf(r);
        var poster = el('div', { class: 'bragi-poster' });
        if (r.posterUrl) {
            poster.appendChild(el('img', { src: r.posterUrl, alt: '', loading: 'lazy' }));
        } else {
            poster.appendChild(el('span', { class: 'bragi-poster-fallback', text: r.title }));
        }
        var action;
        if (st) {
            action = el('span', { class: 'bragi-badge ' + st.cls, text: st.label });
        } else {
            action = el('button', {
                is: 'emby-button', type: 'button', class: 'bragi-request-btn raised button-submit',
                onclick: function (e) { e.stopPropagation(); openRequestDialog(r, onRequested); }
            }, [el('span', { text: 'Request' })]);
        }
        return el('div', { class: 'bragi-card' }, [
            poster,
            el('div', { class: 'bragi-card-body' }, [
                el('div', { class: 'bragi-card-title', text: r.title }),
                el('div', { class: 'bragi-card-year', text: r.year ? String(r.year) : '' }),
                action
            ])
        ]);
    }

    function grid(results, onRequested) {
        var g = el('div', { class: 'bragi-grid' });
        results.forEach(function (r) { g.appendChild(card(r, onRequested)); });
        return g;
    }

    // ---- request dialog ----

    function openRequestDialog(show, onRequested) {
        var overlay = el('div', { class: 'bragi-dialog-overlay', onclick: function (e) { if (e.target === overlay) overlay.remove(); } });
        var choice = 'all';
        var picked = {};
        var seasonsBox = el('div', { class: 'bragi-seasons bragi-hidden' });
        var options = [
            ['all', 'All seasons', 'Everything that has aired, and new episodes as they come out.'],
            ['latest', 'Latest season', 'Just the newest season.'],
            ['future', 'New episodes only', 'Only episodes that have not aired yet.'],
            ['pick', 'Choose seasons', '']
        ];
        var list = el('div', { class: 'bragi-options', role: 'radiogroup' });
        options.forEach(function (o) {
            var input = el('input', { type: 'radio', name: 'bragi-scope', value: o[0] });
            if (o[0] === 'all') input.checked = true;
            input.addEventListener('change', function () {
                choice = o[0];
                seasonsBox.classList.toggle('bragi-hidden', choice !== 'pick');
            });
            list.appendChild(el('label', { class: 'bragi-option' }, [
                input,
                el('span', {}, [el('strong', { text: o[1] }), o[2] ? el('small', { text: o[2] }) : null])
            ]));
        });
        list.appendChild(seasonsBox);
        api('GET', 'Shows/' + show.tmdbId).then(function (d) {
            (d.seasons || []).forEach(function (n) {
                var cb = el('input', { type: 'checkbox', value: String(n) });
                cb.addEventListener('change', function () { picked[n] = cb.checked; });
                seasonsBox.appendChild(el('label', { class: 'bragi-season' }, [cb, el('span', { text: 'Season ' + n })]));
            });
        }).catch(function () {
            seasonsBox.appendChild(el('small', { text: 'Could not load the seasons.' }));
        });

        var error = el('p', { class: 'bragi-error', role: 'alert' });
        var submit = el('button', { is: 'emby-button', type: 'button', class: 'raised button-submit bragi-primary' }, [el('span', { text: 'Request' })]);
        submit.addEventListener('click', function () {
            var seasons = choice;
            if (choice === 'pick') {
                seasons = Object.keys(picked).filter(function (k) { return picked[k]; }).map(Number);
                if (!seasons.length) { error.textContent = 'Pick at least one season.'; return; }
            }
            submit.disabled = true;
            api('POST', 'Requests', { type: 'tv', tmdbId: show.tmdbId, seasons: seasons }).then(function (rq) {
                overlay.remove();
                show.requested = rq.status === 'pending' ? 'pending' : 'approved';
                toast(rq.status === 'pending' ? 'Requested ' + show.title + '. An admin will approve it.' : 'Requested ' + show.title + '. Bragi is on it.');
                if (onRequested) onRequested();
            }).catch(function (err) {
                submit.disabled = false;
                error.textContent = err.status === 409 ? 'You already asked for this show.' : err.message;
            });
        });

        var dialog = el('div', { class: 'bragi-dialog', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Request ' + show.title }, [
            el('div', { class: 'bragi-dialog-head' }, [
                show.posterUrl ? el('img', { src: show.posterUrl, alt: '' }) : null,
                el('div', {}, [
                    el('h3', { text: show.title + (show.year ? ' (' + show.year + ')' : '') }),
                    el('p', { class: 'bragi-overview', text: show.overview || '' })
                ])
            ]),
            list,
            error,
            el('div', { class: 'bragi-dialog-actions' }, [
                el('button', { is: 'emby-button', type: 'button', class: 'raised button-cancel', onclick: function () { overlay.remove(); } }, [el('span', { text: 'Cancel' })]),
                submit
            ])
        ]);
        overlay.appendChild(dialog);
        (state.panel || document.body).appendChild(overlay);
        submit.focus();
    }

    // ---- panel ----

    function openPanel(tab) {
        if (state.panel) { state.panel.remove(); }
        state.tab = tab || 'discover';
        var content = el('div', { class: 'bragi-content' });
        var search = el('input', { type: 'search', class: 'bragi-search', placeholder: 'Search for a show', 'aria-label': 'Search for a show' });
        var tabs = el('div', { class: 'bragi-tabs', role: 'tablist' });
        function tabButton(id, label) {
            return el('button', {
                type: 'button', role: 'tab', class: 'bragi-tab' + (state.tab === id ? ' bragi-tab-active' : ''), 'data-tab': id,
                onclick: function () { state.tab = id; render(); }
            }, [el('span', { text: label })]);
        }
        tabs.appendChild(tabButton('discover', 'Shows'));
        tabs.appendChild(tabButton('music', 'Music'));
        tabs.appendChild(tabButton('mine', 'My requests'));

        function render() {
            Array.prototype.forEach.call(tabs.children, function (b) { b.classList.toggle('bragi-tab-active', b.getAttribute('data-tab') === state.tab); });
            search.classList.toggle('bragi-hidden', state.tab === 'mine');
            var word = state.tab === 'music' ? 'Search for an artist or an album' : 'Search for a show';
            search.placeholder = word;
            search.setAttribute('aria-label', word);
            content.textContent = '';
            if (state.tab === 'mine') renderMine(content);
            else if (state.tab === 'music') renderMusic(content, search.value.trim());
            else renderDiscover(content, search.value.trim());
        }
        search.addEventListener('input', function () {
            clearTimeout(state.searchTimer);
            state.searchTimer = setTimeout(render, 350);
        });

        var panel = el('div', { class: 'bragi-panel', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Request shows' }, [
            el('div', { class: 'bragi-panel-head' }, [
                el('h2', { text: 'Request' }),
                tabs,
                el('button', { type: 'button', class: 'bragi-close', 'aria-label': 'Close', onclick: closePanel }, [icon('close')])
            ]),
            search,
            content
        ]);
        panel.addEventListener('keydown', function (e) { if (e.key === 'Escape') closePanel(); });
        document.body.appendChild(panel);
        document.body.classList.add('bragi-open');
        state.panel = panel;
        render();
        if (state.tab === 'discover') search.focus();
    }

    function closePanel() {
        if (state.panel) state.panel.remove();
        state.panel = null;
        document.body.classList.remove('bragi-open');
    }

    function loading(box) {
        box.textContent = '';
        box.appendChild(el('div', { class: 'bragi-muted', text: 'Loading…' }));
    }

    function failed(box, err) {
        box.textContent = '';
        box.appendChild(el('div', { class: 'bragi-error', text: err.message || 'Bragi did not answer.' }));
    }

    function renderDiscover(box, q) {
        loading(box);
        var call = q ? api('GET', 'Search?query=' + encodeURIComponent(q)) : api('GET', 'Trending?page=1').then(function (p) { return p.results; });
        call.then(function (results) {
            box.textContent = '';
            box.appendChild(el('h3', { class: 'bragi-section-title', text: q ? 'Results for “' + q + '”' : 'Trending this week' }));
            if (!results.length) {
                box.appendChild(el('div', { class: 'bragi-muted', text: 'No shows found.' }));
                return;
            }
            box.appendChild(grid(results, function () { renderDiscover(box, q); }));
        }).catch(function (err) { failed(box, err); });
    }

    // ---- music ----

    function musicStatus(x) {
        if (x.itemId && (x.status === 'imported' || x.status === 'upgrade_wanted')) return { label: 'In library', cls: 'bragi-ok' };
        if (x.itemId && x.artistId && x.name) return { label: 'Following', cls: 'bragi-ok' };
        if (x.requested === 'available') return { label: 'Available', cls: 'bragi-ok' };
        if (x.requested === 'approved' || (x.itemId && x.monitored)) return { label: 'Requested', cls: 'bragi-busy' };
        if (x.requested === 'pending') return { label: 'Waiting for approval', cls: 'bragi-wait' };
        return null;
    }

    function musicCard(title, sub, image, round, x, body, label, onDone) {
        var st = musicStatus(x);
        var pic = el('div', { class: 'bragi-poster bragi-square' + (round ? ' bragi-round' : '') });
        if (image) pic.appendChild(el('img', { src: image, alt: '', loading: 'lazy' }));
        else pic.appendChild(el('span', { class: 'bragi-poster-fallback', text: title }));
        var action;
        if (st) {
            action = el('span', { class: 'bragi-badge ' + st.cls, text: st.label });
        } else {
            action = el('button', {
                is: 'emby-button', type: 'button', class: 'bragi-request-btn raised button-submit',
                onclick: function (e) {
                    e.stopPropagation();
                    action.disabled = true;
                    api('POST', 'Requests', body).then(function (rq) {
                        toast(rq.status === 'pending' ? title + ' is requested. An admin will approve it.' : title + ' is added.');
                        onDone();
                    }).catch(function (err) { action.disabled = false; toast(err.message); });
                }
            }, [el('span', { text: label })]);
        }
        return el('div', { class: 'bragi-card' }, [pic, el('div', { class: 'bragi-card-body' }, [
            el('div', { class: 'bragi-card-title', text: title }),
            el('div', { class: 'bragi-card-year', text: sub }),
            action
        ])]);
    }

    function albumGrid(albums, onDone) {
        var g = el('div', { class: 'bragi-grid' });
        albums.forEach(function (a) {
            g.appendChild(musicCard(a.title, [a.artist, a.year].filter(Boolean).join(' · '), a.coverUrl, false, a,
                { type: 'music', albumId: a.albumId }, 'Request', onDone));
        });
        return g;
    }

    function renderMusic(box, q) {
        loading(box);
        var again = function () { renderMusic(box, q); };
        if (!q) {
            api('GET', 'Music/Trending').then(function (albums) {
                box.textContent = '';
                box.appendChild(el('h3', { class: 'bragi-section-title', text: 'Popular albums this week' }));
                if (!albums.length) {
                    box.appendChild(el('div', { class: 'bragi-muted', text: 'Search for an artist or an album.' }));
                    return;
                }
                box.appendChild(albumGrid(albums, again));
            }).catch(function (err) { failed(box, err); });
            return;
        }
        api('GET', 'Music/Search?query=' + encodeURIComponent(q)).then(function (r) {
            box.textContent = '';
            if (!r.artists.length && !r.albums.length) {
                box.appendChild(el('div', { class: 'bragi-muted', text: 'Nothing found. Try “Artist - Album” for one album.' }));
                return;
            }
            if (r.artists.length) {
                box.appendChild(el('h3', { class: 'bragi-section-title', text: 'Artists' }));
                var g = el('div', { class: 'bragi-grid' });
                r.artists.forEach(function (a) {
                    g.appendChild(musicCard(a.name, a.disambiguation || a.country || '', a.imageUrl, true, a,
                        { type: 'music', artistId: a.artistId, monitor: 'all' }, 'Follow', again));
                });
                box.appendChild(g);
            }
            if (r.albums.length) {
                box.appendChild(el('h3', { class: 'bragi-section-title', text: 'Albums' }));
                box.appendChild(albumGrid(r.albums, again));
            }
        }).catch(function (err) { failed(box, err); });
    }

    function renderMine(box) {
        loading(box);
        api('GET', 'Requests').then(function (list) {
            box.textContent = '';
            if (!list.length) {
                box.appendChild(el('div', { class: 'bragi-muted', text: 'You have not requested anything yet. Find a show or some music above.' }));
                return;
            }
            var rows = el('div', { class: 'bragi-rows' });
            list.forEach(function (rq) {
                var st = requestLabel(rq);
                var detail = '';
                if (rq.progress && rq.progress.episodes) {
                    var unit = rq.type !== 'music' ? 'episodes' : rq.music && rq.music.kind === 'album' ? 'tracks' : 'albums';
                    detail = rq.progress.imported + ' of ' + rq.progress.episodes + ' ' + unit + ' ready';
                }
                var cancel = null;
                if (rq.status === 'pending') {
                    cancel = el('button', {
                        is: 'emby-button', type: 'button', class: 'raised button-cancel bragi-small', onclick: function () {
                            api('DELETE', 'Requests/' + rq.id).then(function () { toast('Cancelled ' + rq.title + '.'); renderMine(box); })
                                .catch(function (err) { toast(err.message); });
                        }
                    }, [el('span', { text: 'Cancel' })]);
                }
                rows.appendChild(el('div', { class: 'bragi-row' }, [
                    rq.posterUrl ? el('img', { src: rq.posterUrl, alt: '' }) : el('div', { class: 'bragi-row-noimg' }),
                    el('div', { class: 'bragi-row-main' }, [
                        el('div', { class: 'bragi-card-title', text: rq.title + (rq.year ? ' (' + rq.year + ')' : '') }),
                        el('div', { class: 'bragi-muted', text: detail || (rq.reason || '') }),
                        detail && rq.reason ? el('div', { class: 'bragi-muted', text: rq.reason }) : null
                    ]),
                    el('span', { class: 'bragi-badge ' + st.cls, text: st.label }),
                    cancel
                ]));
            });
            box.appendChild(rows);
        }).catch(function (err) { failed(box, err); });
    }

    // ---- header button ----

    function addHeaderButton() {
        // The classic header (still kept in the page, sometimes hidden).
        Array.prototype.forEach.call(document.querySelectorAll('.skinHeader .headerRight'), function (right) {
            if (!right.querySelector('.bragi-header-btn')) right.insertBefore(headerButton(), right.firstChild);
        });
        // The newer React header: the icon buttons sit together on the right.
        Array.prototype.forEach.call(document.querySelectorAll('header.MuiAppBar-root'), function (header) {
            var anchor = header.querySelector('[aria-label="SyncPlay"], [aria-label="Cast to Device"], a[href*="search"]');
            var group = anchor && anchor.parentElement;
            if (!group || group.querySelector('.bragi-header-btn')) return;
            var btn = headerButton();
            btn.className = anchor.className + ' bragi-header-btn bragi-header-btn-mui';
            btn.removeAttribute('is');
            group.insertBefore(btn, group.firstChild);
        });
    }

    function headerButton() {
        return el('button', {
            is: 'paper-icon-button-light', type: 'button', class: 'headerButton headerButtonRight bragi-header-btn paper-icon-button-light',
            title: 'Request shows', 'aria-label': 'Request shows', onclick: function () { openPanel('discover'); }
        }, [icon('request')]);
    }

    // ---- Jellyfin search page ----

    function onSearchPage() {
        return /^#\/?search/.test(location.hash) || /\/search(\.html)?$/.test(location.pathname);
    }

    function searchQuery() {
        var input = document.querySelector('.page:not(.hide) input[type="search"], .page:not(.hide) .searchfields input');
        if (input && input.value) return input.value.trim();
        var m = /[?&]query=([^&]*)/.exec(location.hash || location.search);
        return m ? decodeURIComponent(m[1].replace(/\+/g, ' ')).trim() : '';
    }

    function updateSearchSection() {
        var existing = document.querySelector('.bragi-search-section');
        if (!onSearchPage()) {
            if (existing) existing.remove();
            state.lastSearch = '';
            return;
        }
        var q = searchQuery();
        var page = document.querySelector('.page:not(.hide)');
        if (!page) return;
        if (q === state.lastSearch && existing && page.contains(existing)) return;
        state.lastSearch = q;
        if (existing) existing.remove();
        if (q.length < 2) return;

        var section = el('div', { class: 'bragi-search-section verticalSection' }, [
            el('h2', { class: 'sectionTitle sectionTitle-cards padded-left', text: 'Request from Bragi' })
        ]);
        var body = el('div', { class: 'padded-left padded-right' });
        section.appendChild(body);
        var anchor = page.querySelector('.searchResults') || page.querySelector('.itemsContainer') || page;
        (anchor.parentNode && anchor !== page ? anchor.parentNode : page).appendChild(section);
        loading(body);
        api('GET', 'Search?query=' + encodeURIComponent(q)).then(function (results) {
            if (q !== state.lastSearch) return;
            // Shows Bragi has are in Jellyfin's own results already.
            var missing = results.filter(function (r) { return !r.seriesId; });
            body.textContent = '';
            if (!missing.length) {
                section.remove();
                return;
            }
            body.appendChild(grid(missing, function () { state.lastSearch = ''; updateSearchSection(); }));
        }).catch(function (err) { failed(body, err); });
    }

    // ---- start ----

    function tick() {
        if (!state.enabled) return;
        addHeaderButton();
        clearTimeout(state.searchTimer2);
        state.searchTimer2 = setTimeout(updateSearchSection, 400);
    }

    function start() {
        if (!window.ApiClient || !ApiClient.getCurrentUserId || !ApiClient.getCurrentUserId()) {
            setTimeout(start, 1000);
            return;
        }
        api('GET', 'ClientConfig').then(function (c) {
            if (!c || !c.enabled) return;
            state.enabled = true;
            document.head.appendChild(el('link', { rel: 'stylesheet', href: ApiClient.getUrl('Bragi/ClientStyle') }));
            new MutationObserver(tick).observe(document.body, { childList: true, subtree: true });
            window.addEventListener('hashchange', tick);
            document.addEventListener('input', function (e) {
                if (e.target && e.target.type === 'search' && !e.target.classList.contains('bragi-search')) tick();
            }, true);
            tick();
        }).catch(function () { /* the plugin is not set up; stay hidden */ });
    }

    start();
})();
