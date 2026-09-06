// Popup shell: on open, hand off to the persistent SIDE PANEL and close the
// popup. If the browser refuses (Edge may reject sidePanel.open from this
// context), stay open showing the same UI plus an explicit button — the user
// loses nothing either way.
(async () => {
  const row = document.getElementById('row-sidepanel');
  const btn = document.getElementById('btn-sidepanel');
  const note = document.getElementById('sidepanel-note');

  const openPanel = async () => {
    const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    if (!tab) throw new Error('no active tab');
    await chrome.sidePanel.setOptions({ tabId: tab.id, path: 'panel.html', enabled: true });
    await chrome.sidePanel.open({ tabId: tab.id });
  };

  btn.addEventListener('click', async () => {
    try {
      await openPanel();
      chrome.storage.local.set({ panelErr: '' });
      window.close();
    } catch (e) {
      chrome.storage.local.set({ panelErr: String(e) });
      note.textContent = '打开侧栏失败：' + e;
    }
  });

  try {
    await openPanel();
    window.close();
  } catch (e) {
    chrome.storage.local.set({ panelErr: 'auto: ' + e });
    row.style.display = 'flex';
    note.style.display = 'flex';
    note.textContent =
      '未能自动切到侧栏（' + e + '）。可点上方按钮，或从扩展菜单选“在侧边栏中打开”。';
  }
})();
