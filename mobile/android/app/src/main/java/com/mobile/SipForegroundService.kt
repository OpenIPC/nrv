package com.mobile

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import androidx.core.app.NotificationCompat

/**
 * Служба переднего плана для телефонной линии.
 *
 * Зачем она нужна. Android останавливает приложение, как только его свернули,
 * и вместе с ним рвётся соединение с Asterisk: входящий звонок с калитки
 * приходил бы только если приложение открыто на экране. Постоянное
 * уведомление помечает процесс как нужный пользователю, и система его не
 * трогает — это и есть штатный способ держать телефонную линию включённой
 * (так работают все SIP-клиенты на Android).
 *
 * Типы уведомления (dataSync и microphone) объявлены не для красоты: начиная
 * с Android 14 система требует указывать, чем занята служба. dataSync —
 * поддержание соединения, microphone — разговор, когда экран погас.
 */
class SipForegroundService : Service() {

    companion object {
        private const val CHANNEL_ID = "nvr_sip"
        private const val NOTIFICATION_ID = 7401
        private const val INCOMING_NOTIFICATION_ID = 7402
        private const val WAKELOCK_TAG = "nvr:sip"

        /** Запуск и остановка службы — действиями, а не отдельными методами. */
        const val ACTION_START = "com.mobile.sip.START"
        const val ACTION_UPDATE = "com.mobile.sip.UPDATE"
        const val ACTION_STOP = "com.mobile.sip.STOP"
        const val ACTION_INCOMING = "com.mobile.sip.INCOMING"
        const val ACTION_CLEAR_INCOMING = "com.mobile.sip.CLEAR_INCOMING"

        /** Текст уведомления: его меняет JavaScript, когда идёт звонок. */
        const val EXTRA_TITLE = "title"
        const val EXTRA_TEXT = "text"

        fun start(context: Context, title: String, text: String) {
            val intent = Intent(context, SipForegroundService::class.java).apply {
                action = ACTION_START
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_TEXT, text)
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }

        fun update(context: Context, title: String, text: String) {
            val intent = Intent(context, SipForegroundService::class.java).apply {
                action = ACTION_UPDATE
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_TEXT, text)
            }
            context.startService(intent)
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, SipForegroundService::class.java))
        }

        /**
         * Показывает входящий вызов на весь экран.
         *
         * Обычное уведомление на заблокированном телефоне только звонит, но
         * экрана не показывает — оператор слышит вызов и не видит, кто и
         * куда звонит, пока не разблокирует. Полноэкранное уведомление
         * система показывает сама, поверх блокировки: это штатный способ
         * для звонков.
         */
        fun incoming(context: Context, title: String, text: String) {
            val intent = Intent(context, SipForegroundService::class.java).apply {
                action = ACTION_INCOMING
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_TEXT, text)
            }
            // Служба уже работает, поэтому обычный startService: поднимать
            // её заново через startForegroundService не нужно и вредно.
            context.startService(intent)
        }

        /** Убирает полноэкранное уведомление: вызов принят или завершён. */
        fun clearIncoming(context: Context) {
            context.startService(
                Intent(context, SipForegroundService::class.java).apply {
                    action = ACTION_CLEAR_INCOMING
                },
            )
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    /**
     * Держит процессор и Wi-Fi включёнными, пока работает линия.
     *
     * Без этого телефон перестаёт принимать вызовы, стоит экрану погаснуть:
     * система усыпляет процессор и переводит Wi-Fi в экономичный режим,
     * соединение с сервером рвётся, и вызов приходит только после того, как
     * владелец откроет приложение. Проверено на живом телефоне.
     *
     * PARTIAL_WAKE_LOCK — только процессор, экран не горит. WifiLock в режиме
     * HIGH_PERF не даёт Wi-Fi-модулю засыпать между пакетами: в экономичном
     * режиме пауза между ними доходит до секунд, а вызов столько не ждёт.
     */
    private fun acquireLocks() {
        try {
            if (wakeLock == null) {
                val power = getSystemService(Context.POWER_SERVICE) as PowerManager
                wakeLock = power.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, WAKELOCK_TAG).apply {
                    setReferenceCounted(false)
                    acquire()
                }
            }
        } catch (error: Exception) {
            // Прошивка может не дать блокировку — линия всё равно работает,
            // пока экран включён, и падать из-за этого нельзя.
        }

        try {
            if (wifiLock == null) {
                val wifi = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
                wifiLock = wifi.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, WAKELOCK_TAG)
                wifiLock?.setReferenceCounted(false)
                wifiLock?.acquire()
            }
        } catch (error: Exception) {
            // Без блокировки Wi-Fi вызовы принимаются, пока устройство активно.
        }
    }

    /** Отпускает блокировки: линия больше не нужна. */
    private fun releaseLocks() {
        try {
            wakeLock?.release()
        } catch (error: Exception) {
            // Уже отпущена системой.
        }
        wakeLock = null
        try {
            wifiLock?.release()
        } catch (error: Exception) {
            // Уже отпущена системой.
        }
        wifiLock = null
    }

    override fun onDestroy() {
        releaseLocks()
        super.onDestroy()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }

            ACTION_UPDATE -> {
                val title = intent.getStringExtra(EXTRA_TITLE) ?: defaultTitle()
                val text = intent.getStringExtra(EXTRA_TEXT) ?: defaultText()
                notificationManager().notify(NOTIFICATION_ID, buildNotification(title, text, false))
                return START_STICKY
            }

            ACTION_INCOMING -> {
                val title = intent.getStringExtra(EXTRA_TITLE) ?: "Входящий вызов"
                val text = intent.getStringExtra(EXTRA_TEXT) ?: defaultTitle()
                showIncoming(title, text)
                return START_STICKY
            }

            ACTION_CLEAR_INCOMING -> {
                notificationManager().cancel(INCOMING_NOTIFICATION_ID)
                return START_STICKY
            }

            else -> {
                val title = intent?.getStringExtra(EXTRA_TITLE) ?: defaultTitle()
                val text = intent?.getStringExtra(EXTRA_TEXT) ?: defaultText()
                startAsForeground(title, text)
                return START_STICKY
            }
        }
    }

    /**
     * Перезапуск после того, как система освободила память.
     *
     * Здесь нельзя опираться на переданные данные — их уже нет, поэтому
     * поднимаем уведомление с общим текстом: служба нужна ради соединения,
     * а не ради текста в уведомлении. Если приложение перезапустит линию
     * само, оно обновит текст.
     */
    override fun onTaskRemoved(rootIntent: Intent?) {
        // Пусто намеренно: служба продолжает работать, когда пользователь
        // смахивает приложение из списка последних задач. Именно ради этого
        // она и заведена.
        super.onTaskRemoved(rootIntent)
    }

    private fun startAsForeground(title: String, text: String) {
        createChannel()
        acquireLocks()
        val notification = buildNotification(title, text, true)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val types = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC or
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE
            } else {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
            }
            startForeground(NOTIFICATION_ID, notification, types)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    /**
     * Уведомление входящего вызова — на весь экран.
     *
     * Канал отдельный и IMPORTANCE_HIGH: у постоянного уведомления о линии
     * важность LOW (иначе оно бы звонило при каждом входе в приложение), а
     * полноэкранный показ система разрешает только для важных уведомлений.
     */
    private fun showIncoming(title: String, text: String) {
        createIncomingChannel()
        val launchIntent = packageManager.getLaunchIntentForPackage(packageName)
        val pendingIntent = launchIntent?.let {
            PendingIntent.getActivity(
                this,
                1,
                it.apply {
                    addFlags(
                        Intent.FLAG_ACTIVITY_NEW_TASK or
                            Intent.FLAG_ACTIVITY_SINGLE_TOP or
                            Intent.FLAG_ACTIVITY_CLEAR_TOP,
                    )
                },
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
        }

        val builder = NotificationCompat.Builder(this, "${CHANNEL_ID}_calls")
            .setContentTitle(title)
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_phone_call)
            .setPriority(NotificationCompat.PRIORITY_MAX)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setOngoing(true)
            .setAutoCancel(false)

        if (pendingIntent != null) {
            builder.setContentIntent(pendingIntent)
            // Именно это показывает экран поверх блокировки — без него на
            // заблокированном телефоне только звонит, а кто звонит и кнопки
            // ответа не видно.
            builder.setFullScreenIntent(pendingIntent, true)
        }

        notificationManager().notify(INCOMING_NOTIFICATION_ID, builder.build())
    }

    private fun createIncomingChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = notificationManager()
        val id = "${CHANNEL_ID}_calls"
        if (manager.getNotificationChannel(id) != null) return
        val channel = NotificationChannel(
            id,
            "Входящие вызовы",
            NotificationManager.IMPORTANCE_HIGH,
        )
        channel.description = "Показывает входящий вызов с домофона на весь экран"
        channel.setSound(null, null)
        manager.createNotificationChannel(channel)
    }

    private fun buildNotification(title: String, text: String, ongoing: Boolean): Notification {        // Нажатие на уведомление возвращает в приложение, а не открывает
        // новый экран: у него уже есть свой режим единственного экземпляра.
        val launchIntent = packageManager.getLaunchIntentForPackage(packageName)
        val pendingIntent = launchIntent?.let {
            PendingIntent.getActivity(
                this,
                0,
                it,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
        }

        val builder = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(title)
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_phone_call)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setOngoing(ongoing)

        if (pendingIntent != null) {
            builder.setContentIntent(pendingIntent)
        }
        return builder.build()
    }

    private fun createChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = notificationManager()
        if (manager.getNotificationChannel(CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            "Телефонная линия",
            // LOW: уведомление висит постоянно, и звук при его появлении
            // раздражал бы каждый раз при входе в приложение.
            NotificationManager.IMPORTANCE_LOW,
        )
        channel.description = "Держит приложение на связи с сервером, чтобы приходили звонки"
        manager.createNotificationChannel(channel)
    }

    private fun notificationManager(): NotificationManager =
        getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    private fun defaultTitle(): String = "Домофония"

    private fun defaultText(): String = "Линия на связи"

    /** Блокировки, которые держат линию живой при погашенном экране. */
    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null
}
