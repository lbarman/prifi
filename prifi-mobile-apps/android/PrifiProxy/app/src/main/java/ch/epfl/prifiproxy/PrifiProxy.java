package ch.epfl.prifiproxy;

import android.app.Application;
import android.content.Context;
import android.content.SharedPreferences;
import android.util.Log;

import prifiMobile.PrifiMobile;

/**
 * Created by junchen on 23.03.18.
 */

public class PrifiProxy extends Application {

    private static Context mContext;

    @Override
    public void onCreate() {
        super.onCreate();
        mContext = getApplicationContext();

        initPrifiConfig();
    }

    public static Context getContext() {
        return mContext;
    }

    private void initPrifiConfig() {
        final String defaultRelayAddress;
        final int defaultRelayPort;
        final int defaultRelaySocksPort;

        try {
            defaultRelayAddress = PrifiMobile.getRelayAddress();
            defaultRelayPort = longToInt(PrifiMobile.getRelayPort());
            defaultRelaySocksPort = longToInt(PrifiMobile.getRelaySocksPort());
        } catch (Exception e) {
            throw new RuntimeException("Can't Read Configuration Files");
        }

        SharedPreferences prifiPrefs = getSharedPreferences(getString(R.string.prifi_config_shared_preferences), MODE_PRIVATE);
        Boolean isFirstInit = prifiPrefs.getBoolean(getString(R.string.prifi_config_first_init), true);

        if (isFirstInit) {
            SharedPreferences.Editor editor = getSharedPreferences(getString(R.string.prifi_config_shared_preferences), MODE_PRIVATE).edit();
            // Save default values
            editor.putString(getString(R.string.prifi_config_relay_address_default), defaultRelayAddress);
            editor.putInt(getString(R.string.prifi_config_relay_port_default), defaultRelayPort);
            editor.putInt(getString(R.string.prifi_config_relay_socks_port_default), defaultRelaySocksPort);
            // Copy default values
            editor.putString(getString(R.string.prifi_config_relay_address), defaultRelayAddress);
            editor.putInt(getString(R.string.prifi_config_relay_port), defaultRelayPort);
            editor.putInt(getString(R.string.prifi_config_relay_socks_port), defaultRelaySocksPort);
            // Set isFirstInit false
            editor.putBoolean(getString(R.string.prifi_config_first_init), false);
            // apply
            editor.apply();
        } else {
            // Modify PrifiConfig Memory Call setters with existing values
        }
    }

    private int longToInt(long l) {
        if (l < Integer.MIN_VALUE || l > Integer.MAX_VALUE) {
            throw new IllegalArgumentException(l + " Invalid Port");
        }
        return (int) l;
    }

}