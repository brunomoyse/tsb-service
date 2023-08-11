<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::create('product_tag_translations', function (Blueprint $table) {
            $table->uuid('id')->primary()->default(DB::raw('gen_random_uuid()'));
            $table->timestamps();
            $table->enum('language', ['EN', 'FR']);
            $table->uuid('product_tag_id');
            $table->foreign('product_tag_id')->references('id')->on('product_tags')->onDelete('cascade');
            $table->string('name');
            $table->unique(['product_tag_id', 'language']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('product_tag_translations');
    }
};
