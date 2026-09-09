(() => {
  const online = document.getElementById('live-online');
  const connected = document.getElementById('live-connected');
  const sessions = document.getElementById('live-sessions');
  const state = document.getElementById('live-updated');
  if (!online || !connected || !sessions || !state) return;

  const count = value => Number.isSafeInteger(value) && value >= 0 ? String(value) : null;
  const refresh = async () => {
    try {
      const response = await fetch('/api/public-stats', {cache: 'no-store', credentials: 'omit'});
      if (!response.ok) throw new Error('status');
      const data = await response.json();
      const nextOnline = count(data.onlineDevices);
      const nextConnected = count(data.connectedDevices);
      const nextSessions = count(data.activeSessions);
      if (nextOnline === null || nextConnected === null || nextSessions === null) throw new Error('data');
      online.textContent = nextOnline;
      connected.textContent = nextConnected;
      sessions.textContent = nextSessions + ' 个会话，控制端与被控端合计';
      state.textContent = '刚刚更新';
      state.classList.remove('stale');
    } catch (_) {
      state.textContent = '更新暂缓';
      state.classList.add('stale');
    }
  };

  refresh();
  window.setInterval(refresh, 30000);
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) refresh();
  });
})();
