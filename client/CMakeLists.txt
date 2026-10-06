cmake_minimum_required(VERSION 3.16)

project(nvr-wall VERSION 0.1 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)

# Quick — окна и QML, Network — HTTP к API.
#
# Multimedia намеренно НЕ подключён: кадры рисует свой элемент
# (src/video/VideoItem.cpp), а в Qt 6.4 приёмник у VideoOutput всё равно
# только для чтения. Лишняя зависимость попала в поставку как требование
# Windows загрузить Qt6Multimedia.dll — и приложение падало при запуске,
# не успев ничего показать.
find_package(Qt6 REQUIRED COMPONENTS Quick Network Qml)

# GStreamer берём разными путями под Linux и Windows.
#
# В Linux он ставится из репозитория и всегда оставляет pkg-config, поэтому
# версия и пути берутся оттуда. Официальная сборка для Windows (MSI)
# pkg-config не оставляет: путь к ней задаётся переменной GSTREAMER_ROOT,
# а по умолчанию — переменной окружения, которую она прописывает при
# установке.
if(WIN32)
    if(NOT GSTREAMER_ROOT)
        set(GSTREAMER_ROOT "$ENV{GSTREAMER_1_0_ROOT_MSVC_X86_64}")
    endif()

    if(NOT GSTREAMER_ROOT)
        message(FATAL_ERROR
            "Не найден GStreamer. Установите его с gstreamer.freedesktop.org "
            "(пакеты MSVC: runtime и development) или укажите путь явно: "
            "cmake -DGSTREAMER_ROOT=C:/gstreamer/1.0/msvc_x86_64 ..")
    endif()

    # Проверяем библиотеки сразу: путь без суффикса версии собирается, но
    # упирается в линковку, и причину искать долго.
    if(NOT EXISTS "${GSTREAMER_ROOT}/lib/gstreamer-1.0.lib")
        message(FATAL_ERROR
            "В ${GSTREAMER_ROOT} нет библиотек GStreamer. Проверьте, что "
            "установлен пакет *-devel (development) той же разрядности, "
            "что и сборка клиента.")
    endif()

    set(GST_INCLUDE_DIRS
        "${GSTREAMER_ROOT}/include/gstreamer-1.0"
        "${GSTREAMER_ROOT}/include/glib-2.0"
        "${GSTREAMER_ROOT}/lib/glib-2.0/include")
    set(GST_LIBRARY_DIRS "${GSTREAMER_ROOT}/lib")
    # glib и gobject перечисляем явно: в Linux их подтягивает pkg-config как
    # зависимости gstreamer, а MSVC-пакет этого не делает, и сборка падает
    # на неразрешённых символах (g_free, g_signal_connect_data и прочих).
    set(GST_LIBRARIES
        gstreamer-1.0
        gstapp-1.0
        gstvideo-1.0
        glib-2.0
        gobject-2.0
        gmodule-2.0
        gthread-2.0)
else()
    find_package(PkgConfig REQUIRED)
    pkg_check_modules(GST REQUIRED IMPORTED_TARGET
        gstreamer-1.0 gstreamer-app-1.0 gstreamer-video-1.0)
endif()

qt_add_executable(nvr-wall
    src/main.cpp
    src/api/ApiClient.cpp
    src/video/StreamPlayer.cpp
    src/video/VideoItem.cpp
    src/wall/WallProfile.cpp
)

# Пометка для кода: по ней приложение выставит пути к плагинам рядом
# с исполняемым файлом, чтобы поставка была переносимой (папка с exe
# содержит и Qt, и GStreamer).
if(WIN32)
    target_compile_definitions(nvr-wall PRIVATE NVR_WINDOWS_BUILD)
endif()

target_include_directories(nvr-wall PRIVATE src)

target_link_libraries(nvr-wall PRIVATE
    Qt6::Quick
    Qt6::Network
    Qt6::Qml
)

if(WIN32)
    target_include_directories(nvr-wall PRIVATE ${GST_INCLUDE_DIRS})
    target_link_directories(nvr-wall PRIVATE ${GST_LIBRARY_DIRS})
    target_link_libraries(nvr-wall PRIVATE ${GST_LIBRARIES})
else()
    target_link_libraries(nvr-wall PRIVATE PkgConfig::GST)
endif()

# Интерфейс кладём рядом с исполняемым файлом. На первом этапе этого
# достаточно; ресурсный файл (.qrc) понадобится, когда клиент начнут
# поставлять одним файлом без каталога qml рядом.
add_custom_command(TARGET nvr-wall POST_BUILD
    COMMAND ${CMAKE_COMMAND} -E copy_directory
        ${CMAKE_CURRENT_SOURCE_DIR}/qml
        $<TARGET_FILE_DIR:nvr-wall>/qml
)
